package selfupdate

import (
	"net/http"
	"net/http/httptest"
)

// Use the real HTTP request path without binding a socket in restricted lanes.
type testHTTP struct {
	URL     string
	handler http.Handler
}

func newTestHTTP(handler http.Handler) *testHTTP {
	return &testHTTP{URL: "https://releases.test", handler: handler}
}
func (*testHTTP) Close()                 {}
func (s *testHTTP) Client() *http.Client { return &http.Client{Transport: s} }
func (s *testHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, r)
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	return recorder.Result(), nil
}
