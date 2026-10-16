package config

import (
	"os"
	"testing"
)

func TestAPIDocsEnvironment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value string
		want  bool
	}{{"", false}, {"1", true}, {"0", false}, {"true", true}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			var cfg Base
			err := LoadFrom(&cfg, func(key string) (string, bool) { return tc.value, key == "SPINUP_API_DOCS" && tc.value != "" }, os.ReadFile)
			if err != nil || cfg.APIDocs != tc.want {
				t.Fatalf("APIDocs=%v, err=%v", cfg.APIDocs, err)
			}
		})
	}
}
