package app

import (
	"encoding/json"
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
		{env: "", want: http.StatusNotFound},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Parallel()
			a := New(WithLogger(discardLogger()), WithConfig(config.Base{Env: tc.env}))
			for _, path := range []string{"/_dev", "/_dev/", "/_dev/requests.json"} {
				w := httptest.NewRecorder()
				a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
				if w.Code != tc.want {
					t.Errorf("%s: status = %d, want %d", path, w.Code, tc.want)
				}
				if tc.env != "dev" {
					continue
				}
				if path != "/_dev/requests.json" {
					if !strings.Contains(w.Body.String(), "Development") {
						t.Error(w.Body.String())
					}
					continue
				}
				var requests []struct{ Path string }
				if err := json.Unmarshal(w.Body.Bytes(), &requests); err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || len(requests) != 2 || requests[0].Path != "/_dev" || requests[1].Path != "/_dev/" {
					t.Fatalf("unexpected request log: %s (%s)", w.Body, w.Header().Get("Content-Type"))
				}
			}
		})
	}
}
