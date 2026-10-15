package testkit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Do sends one request to h and returns the recorded response. When body is
// not nil, the request has the header "Content-Type: application/json".
func Do(t testing.TB, h http.Handler, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// DecodeJSON decodes the body of rec into a T. It fails the test when the
// body is not valid JSON for T.
func DecodeJSON[T any](t testing.TB, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("testkit: decode JSON response (status %d): %v\nbody: %s", rec.Code, err, rec.Body.String())
	}
	return v
}
