package mcp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestLegacyRequestContext(t *testing.T) {
	installKeyReader(t)
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancelDeadline := context.WithDeadline(requestContext(apikeys.ScopeWrite, "staff"), time.Now().Add(time.Second))
			defer cancelDeadline()
			ctx, cancel := context.WithCancelCause(ctx)
			defer cancel(nil)
			wantDeadline, _ := ctx.Deadline()
			cause := errors.New("request ended")
			started := make(chan context.Context, 1)
			s := newServer(t, Config{})
			Tool(s, ToolDef{Name: "wait", Description: "Wait for the request"}, func(ctx context.Context, _ struct{}) (struct{}, error) {
				started <- ctx
				// The timer prevents a broken implementation from leaking a handler.
				select {
				case <-ctx.Done():
				case <-time.After(3 * time.Second):
				}
				return struct{}{}, nil
			})
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				rpc(t, route(s), ctx, "tools/call", `{"name":"wait","arguments":{}}`)
			}()
			toolCtx := <-started
			if got, ok := toolCtx.Deadline(); !ok || !got.Equal(wantDeadline) {
				t.Errorf("tool deadline = %v, %v, want %v", got, ok, wantDeadline)
			}
			if mode == "cancel" {
				cancel(cause)
			} else {
				cause = context.DeadlineExceeded
			}
			select {
			case <-finished:
				if !errors.Is(context.Cause(toolCtx), cause) {
					t.Errorf("tool cause = %v, want %v", context.Cause(toolCtx), cause)
				}
			case <-time.After(2 * time.Second):
				t.Error("tool did not stop when the request ended")
				<-finished
			}
		})
	}
}

type wireDate struct{ time.Time }

func (d wireDate) MarshalJSON() ([]byte, error) { return json.Marshal(d.Format(time.DateOnly)) }
func (d *wireDate) UnmarshalJSON(b []byte) error {
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return err
	}
	var err error
	d.Time, err = time.Parse(time.DateOnly, text)
	return err
}

type wireUUID [16]byte

func (id wireUUID) MarshalText() ([]byte, error) {
	text := hex.EncodeToString(id[:])
	return []byte(text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]), nil
}
func (id *wireUUID) UnmarshalText(b []byte) error {
	_, err := hex.Decode(id[:], []byte(strings.ReplaceAll(string(b), "-", "")))
	return err
}

func TestToolStringSchemas(t *testing.T) {
	installKeyReader(t)
	t.Run("date and UUID", func(t *testing.T) {
		type input struct {
			Due wireDate `json:"due"`
			ID  wireUUID `json:"id"`
		}
		want := input{Due: wireDate{time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)}, ID: wireUUID{15: 1}}
		s := newServer(t, Config{})
		called := false
		Tool(s, ToolDef{Name: "strings", Description: "Read string values"}, func(_ context.Context, in input) (input, error) {
			called = true
			if in != want {
				t.Errorf("input = %+v, want %+v", in, want)
			}
			return in, nil
		})
		assertStringSchema(t, s, "due", "id")
		w := rpc(t, route(s), requestContext(apikeys.ScopeWrite, "staff"), "tools/call", `{"name":"strings","arguments":{"due":"2026-11-06","id":"00000000-0000-0000-0000-000000000001"}}`)
		if !called || !strings.Contains(w.Body.String(), `"structuredContent":{"due":"2026-11-06","id":"00000000-0000-0000-0000-000000000001"}`) {
			t.Fatalf("tool response: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("UUID", func(t *testing.T) {
		type input struct {
			ID wireUUID `json:"id"`
		}
		s := newServer(t, Config{})
		Tool(s, ToolDef{Name: "uuid", Description: "Read a UUID"}, func(_ context.Context, in input) (input, error) { return in, nil })
		assertStringSchema(t, s, "id")
	})
}

func assertStringSchema(t *testing.T, s *Server, fields ...string) {
	t.Helper()
	w := rpc(t, route(s), requestContext(apikeys.ScopeWrite, "staff"), "tools/list", `{}`)
	type schema struct {
		Properties map[string]struct{ Type string } `json:"properties"`
	}
	var body struct {
		Result struct {
			Tools []struct{ InputSchema, OutputSchema schema }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Result.Tools) != 1 {
		t.Fatalf("tools/list: %s", w.Body.String())
	}
	for _, schema := range []schema{body.Result.Tools[0].InputSchema, body.Result.Tools[0].OutputSchema} {
		for _, field := range fields {
			if schema.Properties[field].Type != "string" {
				t.Errorf("%s schema is not a string: %s", field, w.Body.String())
			}
		}
	}
}

func TestStrictRequestBody(t *testing.T) {
	installKeyReader(t)
	const invalidDetail = "The request body is not valid JSON. Try: send one JSON-RPC message without duplicate names."
	for _, tc := range []struct {
		name, body, detail string
		status             int
	}{
		{"duplicate", `{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/list","params":{}}`, invalidDetail, 400},
		{"nested duplicate", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read","arguments":{"a":1,"a":2}}}`, invalidDetail, 400},
		{"invalid UTF-8", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{\"bad\":\"\xff\"}}", invalidDetail, 400},
		{"over limit", strings.Repeat(" ", 257), "The request body is larger than 256 bytes. Try: send a smaller request.", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, Config{MaxBodyBytes: 256})
			w := rawRPC(route(s), requestContext(apikeys.ScopeWrite, "staff"), "2025-11-25", tc.body)
			var p httpx.Problem
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				t.Fatalf("problem: %d %s: %v", w.Code, w.Body.String(), err)
			}
			if w.Code != tc.status || p.Status != tc.status || p.Detail != tc.detail || w.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("problem: %d %+v", w.Code, p)
			}
		})
	}
}

type countedBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *countedBody) Close() error { b.closed = true; return nil }

func TestBodyReadBoundAndOrder(t *testing.T) {
	installKeyReader(t)
	h := route(newServer(t, Config{MaxBodyBytes: 256}))
	for _, tc := range []struct {
		name, origin string
		scope        apikeys.Scope
		status, read int
	}{
		{"origin first", "https://other.example", "", 403, 0},
		{"key second", "", "", 401, 0},
		{"bounded read", "", apikeys.ScopeWrite, 413, 257},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &countedBody{Reader: strings.NewReader(strings.Repeat(" ", 1024))}
			r := httptest.NewRequestWithContext(requestContext(tc.scope, "staff"), http.MethodPost, Path, nil)
			r.Body = body
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || body.read != tc.read || (tc.read > 0 && !body.closed) {
				t.Fatalf("status=%d read=%d closed=%v", w.Code, body.read, body.closed)
			}
		})
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	body += strings.Repeat(" ", 256-len(body))
	w := rawRPC(h, requestContext(apikeys.ScopeWrite, "staff"), "2025-11-25", body)
	if w.Code != http.StatusOK {
		t.Fatalf("exact limit: %d %s", w.Code, w.Body.String())
	}
}

func rawRPC(h http.Handler, ctx context.Context, protocol, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, Path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", protocol)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestBatchRequestIDs(t *testing.T) {
	installKeyReader(t)
	parses := 0
	parse := parseRequestIDs
	parseRequestIDs = func(body []byte) *requestIDs {
		parses++
		return parse(body)
	}
	t.Cleanup(func() { parseRequestIDs = parse })
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	s := newServer(t, Config{}, WithTracerProvider(tp))
	type input struct {
		Index int `json:"index"`
	}
	var mu sync.Mutex
	wantIDs := make(map[trace.SpanID]string)
	Tool(s, ToolDef{Name: "read", Description: "Read", ReadOnly: true}, func(ctx context.Context, in input) (struct{}, error) {
		mu.Lock()
		wantIDs[trace.SpanFromContext(ctx).SpanContext().SpanID()] = fmt.Sprintf("id-%d", in.Index)
		mu.Unlock()
		return struct{}{}, nil
	})
	var calls []string
	const n = 8
	for i := range n {
		calls = append(calls, fmt.Sprintf(`{"jsonrpc":"2.0","id":"id-%d","method":"tools/call","params":{"name":"read","arguments":{ "index" : %d }}}`, i, i))
	}
	// Only protocols before 2025-06-18 accept JSON-RPC batches.
	w := rawRPC(route(s), requestContext(apikeys.ScopeRead, "staff"), "2025-03-26", "["+strings.Join(calls, ",")+"]")
	if w.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", w.Code, w.Body.String())
	}
	if parses != 1 {
		t.Errorf("body parses = %d, want 1", parses)
	}
	spans := exporter.GetSpans()
	if len(spans) != n {
		t.Fatalf("spans = %d, want %d", len(spans), n)
	}
	for _, span := range spans {
		attrs := attribute.NewSet(span.Attributes...)
		id, _ := attrs.Value("jsonrpc.request.id")
		if want := wantIDs[span.SpanContext.SpanID()]; want == "" || id.AsString() != want {
			t.Errorf("span %s ID = %q, want %q", span.Name, id.AsString(), want)
		}
	}
}
