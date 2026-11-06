package flags

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/auth"
)

func TestMiddlewareSubject(t *testing.T) {
	for _, signedIn := range []bool{false, true} {
		t.Run(map[bool]string{false: "anonymous", true: "signed in"}[signedIn], func(t *testing.T) {
			f := newTestFlags(t)
			f.Replace(NewSnapshot(f.Declared(), map[string]Rule{"new_checkout": {Roles: []string{"staff"}}}, time.Time{}))
			h := f.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got, ok := SubjectFrom(r.Context())
				want := Subject{Anonymous: true}
				if signedIn {
					want = Subject{UserID: 42, Role: "staff"}
				}
				if !ok || got != want || Enabled(r.Context(), "new_checkout") != signedIn {
					t.Fatalf("subject = %+v, %v, evaluation = %+v", got, ok, Evaluate(r.Context(), "new_checkout"))
				}
			}))
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if signedIn {
				r = r.WithContext(auth.WithUser(r.Context(), auth.User{ID: 42, Role: "staff"}))
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
		})
	}
}

func TestMiddlewareKeepsOneSnapshot(t *testing.T) {
	f := newTestFlags(t)
	f.Replace(NewSnapshot(f.Declared(), map[string]Rule{"new_checkout": {}}, time.Time{}))
	calls := 0
	h := f.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		before := Enabled(r.Context(), "new_checkout")
		f.Replace(NewSnapshot(f.Declared(), map[string]Rule{"new_checkout": {Enabled: true}}, time.Time{}))
		after := Enabled(r.Context(), "new_checkout")
		if before != after || before != (calls > 0) {
			t.Fatalf("request %d: before = %v, after = %v", calls, before, after)
		}
		calls++
	}))
	for range 2 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	}
}
