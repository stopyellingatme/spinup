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
		{name: "body null", body: `null`, required: []string{"name"}, status: 422},
		{name: "zeros", body: `{"name":"","flag":false,"count":0,"data":{}}`, required: []string{"name", "flag", "count", "data"}},
		{name: "optional null", body: `{"name":"","note":null}`, required: []string{"name"}},
		{name: "optional omitted", body: `{"name":""}`, required: []string{"name"}},
		{name: "exact key", body: `{"NAME":"value"}`, required: []string{"name"}, missing: []FieldError{{Field: "name", Message: "is required"}}, status: 422},
		{name: "duplicate null", body: `{"name":"value","name":null}`, required: []string{"name"}, status: 400},
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

func TestCheckJSONBody(t *testing.T) {
	t.Parallel()
	const invalid = "The request body must contain valid JSON with only known fields and correct value types."
	const object = "The request body must be a JSON object."
	rules := BodyRules{Required: []string{"title"}, Known: []string{"title", "body", "published", "nested"}}
	for _, tc := range []struct {
		name, body string
		rules      BodyRules
		status     int
		detail     string
		fields     []FieldError
		limit      int64
	}{
		{name: "resource", body: `{"title":"x","body":"text","published":false}`, rules: rules},
		{name: "escaped known name", body: `{"\u0074itle":"x"}`, rules: rules},
		{name: "known only", body: `{"title":null}`, rules: BodyRules{Known: []string{"title"}}},
		{name: "missing required", body: `{}`, rules: rules, status: 422, detail: "The request has invalid values.", fields: []FieldError{{Field: "title", Message: "is required"}}},
		{name: "null required", body: `{"title":null}`, rules: rules, status: 422, detail: "The request has invalid values.", fields: []FieldError{{Field: "title", Message: "is required"}}},
		{name: "sorted", body: `{"z":null}`, rules: BodyRules{Required: []string{"z", "a"}}, status: 422, detail: "The request has invalid values.", fields: []FieldError{{Field: "a", Message: "is required"}, {Field: "z", Message: "is required"}}},
		{name: "case variant", body: `{"TITLE":"x"}`, rules: rules, status: 400, detail: invalid},
		{name: "unknown member", body: `{"title":"x","other":1}`, rules: rules, status: 400, detail: invalid},
		{name: "unknown nested member allowed", body: `{"title":"x","nested":{"other":1}}`, rules: rules},
		{name: "duplicate", body: `{"title":"x","title":"y"}`, rules: rules, status: 400, detail: invalid},
		{name: "nested duplicate", body: `{"title":"x","nested":{"a":1,"a":2}}`, rules: rules, status: 400, detail: invalid},
		{name: "duplicate inside array", body: `{"title":"x","nested":[{"a":1,"a":2}]}`, rules: rules, status: 400, detail: invalid},
		{name: "invalid UTF-8", body: "{\"title\":\"\xff\"}", rules: rules, status: 400, detail: invalid},
		{name: "invalid UTF-8 name", body: "{\"\xff\":1}", status: 400, detail: invalid},
		{name: "trailing garbage", body: `{} x`, rules: rules, status: 400, detail: invalid},
		{name: "null root", body: `null`, rules: rules, status: 422, detail: object},
		{name: "array root", body: `[]`, rules: rules, status: 422, detail: object},
		{name: "scalar root", body: `42`, rules: rules, status: 422, detail: object},
		{name: "array trailing garbage", body: `[] x`, rules: rules, status: 400, detail: invalid},
		{name: "array nested duplicate", body: `[{"a":1,"a":2}]`, status: 400, detail: invalid},
		{name: "null trailing garbage", body: `null x`, status: 400, detail: invalid},
		{name: "size limit", body: `{"title":"x"}`, rules: rules, status: 413, detail: "The request body exceeds the size limit.", limit: 5},
		{name: "trailing size limit", body: `{}        `, status: 413, detail: "The request body exceeds the size limit.", limit: 3},
		{name: "non-object size limit", body: `[]        `, status: 413, detail: "The request body exceeds the size limit.", limit: 3},
		{name: "empty without rules"},
		{name: "whitespace without rules", body: " \n\t"},
		{name: "truncated without rules", body: `{`, status: 400, detail: invalid},
		{name: "non-object without rules", body: `true`, status: 422, detail: object},
		{name: "empty known", rules: BodyRules{Known: []string{"title"}}, status: 400, detail: "The request body must contain one JSON value."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := &trackedBody{Reader: strings.NewReader(tc.body)}
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
			r.Body = body
			if tc.limit > 0 {
				r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, tc.limit)
			}
			err := CheckJSONBody(r, tc.rules)
			if tc.status == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else if p := ProblemFrom(err); p.Status != tc.status || p.Detail != tc.detail || !reflect.DeepEqual(p.Errors, tc.fields) {
				t.Fatalf("problem %+v, want status %d, detail %q, fields %+v", p, tc.status, tc.detail, tc.fields)
			}
			if tc.limit == 0 {
				data, err := io.ReadAll(r.Body)
				if err != nil || string(data) != tc.body {
					t.Fatalf("body %q, error %v, want %q", data, err, tc.body)
				}
			}
			if body.closed {
				t.Fatal("check closed the body")
			}
			if err := r.Body.Close(); err != nil || !body.closed {
				t.Fatalf("close error %v, closed %v", err, body.closed)
			}
		})
	}
}

func TestCheckJSONBodyNilBody(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Body = nil
	if err := CheckJSONBody(r, BodyRules{}); err != nil {
		t.Fatal(err)
	}
	if p := ProblemFrom(CheckJSONBody(r, BodyRules{Required: []string{"title"}})); p.Status != 400 {
		t.Fatalf("problem %+v, want 400", p)
	}
}
