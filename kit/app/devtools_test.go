package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/kit/config"
)

func TestDevtoolsMountsOnlyInDevelopment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		env  string
		want int
	}{
		{env: "dev", want: http.StatusOK},
		{env: "production", want: http.StatusNotFound},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Parallel()
			a := New(WithLogger(discardLogger()), WithConfig(config.Base{Env: tc.env}))
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_dev", nil))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if tc.env == "dev" && !strings.Contains(w.Body.String(), "Development") {
				t.Fatal(w.Body.String())
			}
		})
	}
}
