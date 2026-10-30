package deploy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

// TestPreflightServiceSecretMissing fails the preflight when a service names a secret
// that the secrets of the environment do not hold (issue #37, review 4).
func TestPreflightServiceSecretMissing(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	withService(t, x, false)
	path := filepath.Join(x.root, "spinup.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	toml := strings.Replace(string(data), `secrets = ["API_TOKEN"]`, `secrets = ["OCR_LICENSE_KEY"]`, 1)
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	x.expectLocalGit()
	err = x.run()
	var ue *ui.Error
	if !errors.As(err, &ue) || !strings.Contains(ue.Why, "OCR_LICENSE_KEY of service ocr") || !strings.Contains(ue.Try, "spinup secrets set --env staging OCR_LICENSE_KEY") {
		t.Fatalf("error = %v", err)
	}
	if x.dialed != 0 || len(x.built) != 0 {
		t.Fatal("preflight changed the VM or built images")
	}
}

// TestEnvPairsWorkerReplicas writes WORKER_REPLICAS only with the jobs module, from
// [replicas] worker, with 1 as the default and the minimum (issue #37, review 7).
func TestEnvPairsWorkerReplicas(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		jobs     bool
		replicas map[string]int
		want     string // "" means no WORKER_REPLICAS
	}{
		{"no jobs", false, map[string]int{"worker": 3}, ""},
		{"default", true, nil, "1"},
		{"set", true, map[string]int{"worker": 3}, "3"},
		{"zero", true, map[string]int{"worker": 0}, "1"},
		{"negative", true, map[string]int{"worker": -2}, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := &local{jobs: tc.jobs, envFile: EnvFile{Replicas: tc.replicas}}
			got, found := "", false
			for _, p := range l.envPairs(Blue, nil, "1000", "1000") {
				if p[0] == "WORKER_REPLICAS" {
					got, found = p[1], true
				}
			}
			if found != (tc.want != "") || got != tc.want {
				t.Fatalf("WORKER_REPLICAS = %q (found %v), want %q", got, found, tc.want)
			}
		})
	}
}
