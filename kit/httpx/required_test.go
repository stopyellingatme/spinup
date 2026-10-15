package httpx

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestRequireJSONFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		required   []string
		missing    []FieldError
		status     int
		limit      int64
	}{
		{name: "missing", body: `{}`, required: []string{"name"}, missing: []FieldError{{Field: "name", Message: "is required"}}, status: 422},
		{name: "null", body: `{"name": null}`, required: []string{"name"}, missing: []FieldError{{Field: "name", Message: "is required"}}, status: 422},
		{name: "sorted", body: `{"z": null}`, required: []string{"z", "a"}, missing: []FieldError{{Field: "a", Message: "is required"}, {Field: "z", Message: "is required"}}, status: 422},
		{name: "body null", body: `null`, required: []string{"name"}, missing: []FieldError{{Field: "name", Message: "is required"}}, status: 422},
		{name: "zeros", body: `{"name":"","flag":false,"count":0,"data":{}}`, required: []string{"name", "flag", "count", "data"}},
		{name: "optional null", body: `{"name":"","note":null}`, required: []string{"name"}},
		{name: "optional omitted", body: `{"name":""}`, required: []string{"name"}},
		{name: "exact key", body: `{"NAME":"value"}`, required: []string{"name"}, missing: []FieldError{{Field: "name", Message: "is required"}}, status: 422},
		{name: "duplicate null", body: `{"name":"value","name":null}`, required: []string{"name"}, missing: []FieldError{{Field: "name", Message: "is required"}}, status: 422},
		{name: "empty", required: []string{"name"}, status: 400},
		{name: "syntax", body: `{`, required: []string{"name"}, status: 400},
		{name: "multiple", body: `{} {}`, required: []string{"name"}, status: 400},
		{name: "trailing syntax", body: `{} x`, required: []string{"name"}, status: 400},
		{name: "array", body: `[]`, required: []string{"name"}, status: 422},
		{name: "scalar", body: `true`, required: []string{"name"}, status: 422},
		{name: "size limit", body: `{"name":"value"}`, required: []string{"name"}, status: 413, limit: 5},
		{name: "no required fields", body: `{"note":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tc.body))
			if tc.limit > 0 {
				r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, tc.limit)
			}
			err := RequireJSONFields(r, tc.required)
			if tc.status != 0 {
				var problem *Error
				if !errors.As(err, &problem) || problem.Problem.Status != tc.status {
					t.Fatalf("error %v, want status %d", err, tc.status)
				}
				if !reflect.DeepEqual(problem.Problem.Errors, tc.missing) {
					t.Fatalf("errors %+v, want %+v", problem.Problem.Errors, tc.missing)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r.Body)
			if err != nil || string(data) != tc.body {
				t.Fatalf("body %q, error %v, want %q", data, err, tc.body)
			}
		})
	}
}

func TestRequireJSONFieldsNilBody(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	r.Body = nil
	if p := ProblemFrom(RequireJSONFields(r, []string{"name"})); p.Status != http.StatusBadRequest {
		t.Fatalf("problem %+v, want 400", p)
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestRequireJSONFieldsPreservesClose(t *testing.T) {
	t.Parallel()
	body := &trackedBody{Reader: strings.NewReader(`{"name":"value"}`)}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	r.Body = body
	if err := RequireJSONFields(r, []string{"name"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Body.Close(); err != nil || !body.closed {
		t.Fatalf("close error %v, closed %v", err, body.closed)
	}
}
