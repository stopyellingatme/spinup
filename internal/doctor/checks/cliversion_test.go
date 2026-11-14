package checks

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestCLIVersionCheck(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, cli, pin, message, try string
		root                         bool
		status                       doctor.Status
	}{
		{"outside project", "0.1.0", "", "outside a spinup project", "", false, doctor.Skip},
		{"empty CLI", "", "0.1.0", "development build of spinup", "", true, doctor.Skip},
		{"development CLI", "dev", "0.1.0", "development build of spinup", "", true, doctor.Skip},
		{"lock unreadable", "0.1.0", "", "spinup.lock cannot be read", "", true, doctor.Skip},
		{"unknown pin", "0.1.0", "dev", "spinup.lock records dev, which is not a release version", "", true, doctor.Skip},
		{"match", "0.1.0-alpha.830", "0.1.0-alpha.830", "CLI 0.1.0-alpha.830, spinup.lock 0.1.0-alpha.830", "", true, doctor.OK},
		{"older", "0.1.0-alpha.700", "0.1.0-alpha.830", "spinup 0.1.0-alpha.700 is older than 0.1.0-alpha.830, the version that wrote spinup.lock", "spinup self-update", true, doctor.Warn},
		{"newer", "0.1.0-alpha.830", "0.1.0-alpha.700", "spinup 0.1.0-alpha.830 is newer than 0.1.0-alpha.700, the version that wrote spinup.lock", "spinup upgrade --plan", true, doctor.Warn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := ""
			if tc.root {
				root = t.TempDir()
				if tc.pin != "" {
					lock := manifest.New()
					lock.CLIVersion = tc.pin
					if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
						t.Fatal(err)
					}
				}
			}
			got := cliVersion(context.Background(), &doctor.Context{Root: root, Deps: map[string]any{"cli_version": tc.cli}})
			if got.Status != tc.status || got.Message != tc.message || got.Try != tc.try {
				t.Fatalf("result = %+v", got)
			}
		})
	}
}
