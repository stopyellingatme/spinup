package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/kit/deployx"
)

func TestDeployInfo(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			t.Parallel()
			options := []Option{WithVersion("v42")}
			if enabled {
				options = append([]Option{WithDeployInfo(deployx.Options{})}, options...)
			}
			a := New(options...)
			r := httptest.NewRequestWithContext(context.Background(), "GET", "/_deploy/info", nil)
			r.RemoteAddr = "127.0.0.1:20"
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, r)
			if enabled {
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":"v42"`) {
					t.Fatal(w.Code, w.Body.String())
				}
			} else if w.Code != 404 {
				t.Fatal(w.Code)
			}
		})
	}
}
