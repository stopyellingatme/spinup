package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONStrict(t *testing.T) {
	t.Parallel()
	const invalid = "The request body must contain valid JSON with only known fields and correct value types."
	for _, tc := range []struct {
		name, body, detail string
		limit              int64
		nilBody            bool
		status             int
	}{
		{name: "exact name", body: `{"title":"x"}`},
		{name: "case variant", body: `{"TITLE":"x"}`, status: 400, detail: invalid},
		{name: "duplicate", body: `{"title":"x","title":"y"}`, status: 400, detail: invalid},
		{name: "nested duplicate", body: `{"nested":{"a":1,"a":2}}`, status: 400, detail: invalid},
		{name: "escaped duplicate", body: `{"title":"x","\u0074itle":"y"}`, status: 400, detail: invalid},
		{name: "invalid UTF-8", body: "{\"title\":\"\xff\"}", status: 400, detail: invalid},
		{name: "trailing garbage", body: `{} x`, status: 400, detail: invalid},
		{name: "type", body: `{"title":1}`, status: 400, detail: invalid},
		{name: "truncated object", body: `{`, status: 400, detail: invalid},
		{name: "truncated value", body: `{"title":`, status: 400, detail: invalid},
		{name: "null root", body: `null`},
		{name: "empty", status: 400, detail: "The request body must contain one JSON value."},
		{name: "nil", nilBody: true, status: 400, detail: "The request body must contain one JSON value."},
		{name: "whitespace", body: " \n\t", status: 400, detail: "The request body must contain one JSON value."},
		{name: "two values", body: `{} {}`, status: 400, detail: "The request body must contain only one JSON value."},
		{name: "truncated second value", body: `{} {`, status: 400, detail: invalid},
		{name: "size limit", body: `{"title":"x"}`, limit: 5, status: 413, detail: "The request body exceeds the size limit."},
		{name: "trailing size limit", body: `{}        `, limit: 3, status: 413, detail: "The request body exceeds the size limit."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tc.body))
			if tc.nilBody {
				r.Body = nil
			}
			if tc.limit > 0 {
				r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, tc.limit)
			}
			var dst struct {
				Title  string         `json:"title"`
				Nested map[string]int `json:"nested"`
			}
			err := DecodeJSON(r, &dst)
			if tc.status == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else if p := ProblemFrom(err); p.Status != tc.status || p.Detail != tc.detail {
				t.Fatalf("problem %+v, want status %d and detail %q", p, tc.status, tc.detail)
			}
		})
	}
}

func TestRequireJSONFieldsWithoutRules(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{`, `{} x`, `{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, "{\"a\":\"\xff\"}"} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/", strings.NewReader(body))
			if p := ProblemFrom(RequireJSONFields(r, nil)); p.Status != http.StatusBadRequest {
				t.Fatalf("problem %+v, want 400", p)
			}
		})
	}
}

func TestRequireJSONFieldsRestoresRejectedBody(t *testing.T) {
	t.Parallel()
	for _, data := range []string{`{`, `{}`, `[]`, `{} x`} {
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			body := &trackedBody{Reader: strings.NewReader(data)}
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
			r.Body = body
			if err := RequireJSONFields(r, []string{"title"}); err == nil {
				t.Fatal("accepted invalid body")
			}
			got, err := io.ReadAll(r.Body)
			if err != nil || string(got) != data {
				t.Fatalf("restored body %q, error %v", got, err)
			}
			if err := r.Body.Close(); err != nil || !body.closed {
				t.Fatalf("close error %v, closed %v", err, body.closed)
			}
		})
	}
}
