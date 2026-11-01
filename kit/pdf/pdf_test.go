package pdf_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/pdf"
)

const secret = "SIDECAR-SECRET-TEXT"

// fakeSidecar answers each render request with the handler of its path.
func fakeSidecar(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/good", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(w, "%PDF-1.7\n%fake\n")
	})
	mux.HandleFunc("/html", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(w, "<html>"+secret+"</html>")
	})
	mux.HandleFunc("/type", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "%PDF-1.7")
	})
	mux.HandleFunc("/timeout", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = io.WriteString(w, `{"detail":"`+secret+`"}`)
	})
	mux.HandleFunc("/busy", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"detail":"`+secret+`"}`)
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"detail":"`+secret+`"}`)
	})
	mux.HandleFunc("/oversize", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(w, "%PDF-1.7\n")
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for range 21 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/exact", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf; charset=binary")
		body := bytes.Repeat([]byte("x"), pdf.MaxBytes)
		copy(body, "%PDF-")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(w, "%PDF-1.7\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// trackBody records whether Serve closed the body.
type trackBody struct {
	io.ReadCloser
	closed bool
}

func (b *trackBody) Close() error {
	b.closed = true
	return b.ReadCloser.Close()
}

func TestServe(t *testing.T) {
	t.Parallel()
	srv := fakeSidecar(t)
	for _, tt := range []struct {
		path       string
		status     int
		wantErr    string
		retryAfter string
	}{
		{"/good", http.StatusOK, "", ""},
		{"/exact", http.StatusOK, "", ""},
		{"/html", http.StatusBadGateway, "does not start with %PDF-", ""},
		{"/type", http.StatusBadGateway, `"text/html"`, ""},
		{"/timeout", http.StatusGatewayTimeout, "answered 504: {\"detail\":\"" + secret, ""},
		{"/busy", http.StatusServiceUnavailable, "answered 503", "7"},
		{"/missing", http.StatusBadGateway, "answered 422", ""},
		{"/oversize", http.StatusBadGateway, "larger than", ""},
	} {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+tt.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body := &trackBody{ReadCloser: resp.Body}
			resp.Body = body
			rec := httptest.NewRecorder()
			err = pdf.Serve(rec, resp, nil, "report \"42\"\r\n.pdf")
			if !body.closed {
				t.Error("Serve did not close the body")
			}
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(rec.Body.String(), "%PDF-") || rec.Header().Get("Content-Type") != "application/pdf" {
					t.Fatalf("body %.20q type %q", rec.Body.String(), rec.Header().Get("Content-Type"))
				}
				if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="report 42.pdf"`) {
					t.Errorf("Content-Disposition = %q", cd)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatalf("error = %v, want one with %q and a Try: line", err, tt.wantErr)
			}
			if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "%PDF") {
				t.Errorf("the answer holds sidecar text: %s", rec.Body.String())
			}
			if rec.Header().Get("Content-Type") != "application/problem+json" || rec.Header().Get("Content-Disposition") != "" {
				t.Errorf("headers = %v", rec.Header())
			}
			var p struct {
				Status int    `json:"status"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Status != tt.status || !strings.Contains(p.Detail, "Try:") {
				t.Errorf("problem = %+v, %v", p, err)
			}
			if got := rec.Header().Get("Retry-After"); tt.retryAfter != "" && got != tt.retryAfter {
				t.Errorf("Retry-After = %q, want %q", got, tt.retryAfter)
			}
		})
	}
}

func TestServeNoResponse(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	err := pdf.Serve(rec, nil, nil, "x.pdf")
	if err == nil || rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, Retry-After %q, error %v", rec.Code, rec.Header().Get("Retry-After"), err)
	}
	if !strings.Contains(rec.Body.String(), "Try:") || !strings.Contains(err.Error(), "\nTry:") {
		t.Errorf("no Try: line: %s / %v", rec.Body.String(), err)
	}
}

func TestServeContextDeadline(t *testing.T) {
	t.Parallel()
	srv := fakeSidecar(t)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	rec := httptest.NewRecorder()
	err = pdf.Serve(rec, resp, nil, "x.pdf")
	if rec.Code != http.StatusGatewayTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("status %d, error %v; want 504 and a deadline", rec.Code, err)
	}
}

func TestServeCallError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"deadline", fmt.Errorf("post: %w", context.DeadlineExceeded), http.StatusGatewayTimeout},
		{"connection", errors.New("dial tcp: connection refused"), http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			err := pdf.Serve(rec, nil, tt.err, "x.pdf")
			if rec.Code != tt.want || err == nil || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatalf("status %d, error %v; want %d and a Try: line", rec.Code, err, tt.want)
			}
			if strings.Contains(rec.Body.String(), "connection refused") {
				t.Errorf("the answer leaks the call error: %s", rec.Body.String())
			}
		})
	}
}
