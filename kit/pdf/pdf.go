package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

// MaxBytes is the largest PDF that Serve accepts: 20 MiB.
const MaxBytes = 20 << 20

// magic is the start of every PDF file.
const magic = "%PDF-"

// maxErrorText limits the sidecar text in the returned error.
const maxErrorText = 256

// defaultRetryAfter is the Retry-After of a 503 answer when the sidecar sends none.
const defaultRetryAfter = "5"

// Public problem details. They never hold sidecar text.
const (
	detailUnavailable = "The report service is busy or not available. Try: retry in a moment."
	detailTimeout     = "The report took too long to render. Try: retry later, or ask for a smaller report."
	detailBadGateway  = "The report service sent an invalid answer. Try: retry later. If it fails again, tell the operator."
)

// Serve checks the sidecar response and writes the PDF as an attachment with
// httpx.WriteAttachment. It always closes resp.Body. It accepts only status 200
// with the type application/pdf, reads at most MaxBytes plus one byte, and checks
// that the body starts with %PDF-. A sidecar 504 or a context deadline answers
// 504, a sidecar 503 or a connection error answers 503, and any other failure
// answers 502. The answer holds no sidecar text. The returned error does: log it.
//
// Pass the response and the error of the Render call of the client unchanged.
// When callErr is not nil, Serve closes any response body and answers 504 for a
// deadline or 503 for any other error (a connection error).
func Serve(w http.ResponseWriter, resp *http.Response, callErr error, filename string) error {
	if callErr != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if isTimeout(callErr) {
			writeProblem(w, nil, http.StatusGatewayTimeout)
			return fmt.Errorf("pdf: the report service did not answer in time: %w\nTry: give the request more time, or render a smaller report", callErr)
		}
		writeProblem(w, nil, http.StatusServiceUnavailable)
		return fmt.Errorf("pdf: call the report service: %w\nTry: check that the service runs (docker compose ps) and that its name in the client configuration is right", callErr)
	}
	if resp == nil {
		writeProblem(w, nil, http.StatusServiceUnavailable)
		return errors.New("pdf: the report service sent no response (a connection error)\nTry: check that the service runs (docker compose ps) and that its name in the client configuration is right")
	}
	req := resp.Request
	if resp.Body == nil {
		writeProblem(w, req, http.StatusBadGateway)
		return errors.New("pdf: the report service sent a response without a body\nTry: check the service logs")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		text := errorText(resp.Body)
		status := http.StatusBadGateway
		switch resp.StatusCode {
		case http.StatusGatewayTimeout:
			status = http.StatusGatewayTimeout
		case http.StatusServiceUnavailable:
			status = http.StatusServiceUnavailable
			retry := resp.Header.Get("Retry-After")
			if _, err := strconv.Atoi(retry); err != nil {
				retry = defaultRetryAfter
			}
			w.Header().Set("Retry-After", retry)
		}
		writeProblem(w, req, status)
		return fmt.Errorf("pdf: the report service answered %d: %s\nTry: check the service logs and the render request", resp.StatusCode, text)
	}
	if mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mediaType != "application/pdf" {
		writeProblem(w, req, http.StatusBadGateway)
		return fmt.Errorf("pdf: the report service answered the type %q, not application/pdf\nTry: check that the service is a PDF service (spinup g service <name> --lang python --kind pdf)", clean(resp.Header.Get("Content-Type")))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		if isTimeout(err) || (req != nil && errors.Is(req.Context().Err(), context.DeadlineExceeded)) {
			writeProblem(w, req, http.StatusGatewayTimeout)
			return fmt.Errorf("pdf: read the PDF: %w\nTry: give the request more time, or render a smaller report", err)
		}
		writeProblem(w, req, http.StatusBadGateway)
		return fmt.Errorf("pdf: read the PDF: %w\nTry: check the service logs", err)
	}
	if len(body) > MaxBytes {
		writeProblem(w, req, http.StatusBadGateway)
		return fmt.Errorf("pdf: the PDF is larger than %d bytes\nTry: render a smaller report", MaxBytes)
	}
	if !bytes.HasPrefix(body, []byte(magic)) {
		writeProblem(w, req, http.StatusBadGateway)
		return errors.New("pdf: the body of the report service does not start with %PDF-\nTry: check the template and the service logs")
	}
	httpx.WriteAttachment(w, filename, "application/pdf", body)
	return nil
}

// writeProblem writes a problem with a fixed detail for status.
func writeProblem(w http.ResponseWriter, req *http.Request, status int) {
	detail := detailBadGateway
	switch status {
	case http.StatusServiceUnavailable:
		detail = detailUnavailable
		if w.Header().Get("Retry-After") == "" {
			w.Header().Set("Retry-After", defaultRetryAfter)
		}
	case http.StatusGatewayTimeout:
		detail = detailTimeout
	}
	if req == nil {
		req = &http.Request{}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteProblem(w, req, httpx.Problem{Status: status, Detail: detail})
}

// isTimeout reports whether err is a deadline: a context deadline, an I/O
// deadline, or a network timeout.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// errorText reads the start of an error body for the returned error.
func errorText(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, maxErrorText))
	text := clean(strings.TrimSpace(string(data)))
	if text == "" {
		return "(no body)"
	}
	return text
}

// clean replaces control characters, so the text cannot break a log line.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
