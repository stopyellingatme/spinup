package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheckCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		ok     bool
	}{{"healthy", http.StatusOK, true}, {"unhealthy", http.StatusServiceUnavailable, false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
			a := New(WithLogger(discardLogger()), WithAddr(":"+port))
			if err := a.Run(context.Background(), []string{"healthcheck"}); (err == nil) != tc.ok {
				t.Fatalf("healthcheck error = %v, want ok %v", err, tc.ok)
			}
		})
	}
	a := New(WithLogger(discardLogger()), WithAddr("127.0.0.1:1"))
	if err := a.Run(context.Background(), []string{"healthcheck"}); err == nil {
		t.Fatal("healthcheck passed with no server")
	}
}
