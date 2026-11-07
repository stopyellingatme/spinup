package flagsdrift

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestDrift(t *testing.T) {
	const valid = "[flags.z_old]\ndescription = \"Old\"\nexpires = \"2026-10-15\"\n[flags.a_old]\ndescription = \"Old\"\nexpires = \"2026-10-01\"\n"
	const today = "[flags.demo]\ndescription = \"Demo\"\nexpires = \"2026-11-06\"\n"
	tests := []struct {
		name            string
		project, pack   bool
		config, missing string
		differ          bool
		status          doctor.Status
		message, hint   string
	}{
		{name: "no project", status: doctor.Skip, message: "outside a spinup project: spinup.lock is missing"},
		{name: "no pack", project: true, status: doctor.Skip, message: "no flags module"},
		{name: "invalid", project: true, pack: true, config: "[flags.demo\n", status: doctor.Warn, message: "spinup.toml has invalid flags.", hint: "correct [flags] in spinup.toml and run spinup g flags"},
		{name: "missing Go", project: true, pack: true, config: today, missing: project.FlagsFilePath, status: doctor.Warn, message: project.FlagsFilePath + " does not match [flags] in spinup.toml.", hint: "spinup g flags"},
		{name: "missing TS", project: true, pack: true, config: today, missing: project.FlagsTSFilePath, status: doctor.Warn, message: project.FlagsTSFilePath + " does not match [flags] in spinup.toml.", hint: "spinup g flags"},
		{name: "different Go", project: true, pack: true, config: today, differ: true, status: doctor.Warn, message: project.FlagsFilePath + " does not match [flags] in spinup.toml.", hint: "spinup g flags"},
		{name: "expired", project: true, pack: true, config: valid, status: doctor.Warn, message: "2 flags are past their expires date: a_old (2026-10-01), z_old (2026-10-15).", hint: "remove each [flags.<key>] table and its uses from the code, run spinup g flags, then flags prune --yes"},
		{name: "expires today", project: true, pack: true, config: today, status: doctor.OK, message: "generated flags match [flags] in spinup.toml"},
		{name: "all OK", project: true, pack: true, config: "", status: doctor.OK, message: "generated flags match [flags] in spinup.toml"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.project {
				lock := manifest.New()
				if tc.pack {
					lock.Packs = []string{"flags"}
				}
				if err := lock.Save(filepath.Join(root, project.LockFile)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.pack {
				if err := os.WriteFile(filepath.Join(root, project.ConfigFile), []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
				cfg, err := project.Parse([]byte(tc.config))
				if err == nil {
					for _, f := range []struct {
						path string
						data []byte
					}{{project.FlagsFilePath, project.RenderFlagsFile(cfg.FlagDecls())}, {project.FlagsTSFilePath, project.RenderFlagsTSFile(cfg.FlagDecls(), cfg.AuthRoles())}} {
						if f.path == tc.missing {
							continue
						}
						p := filepath.Join(root, filepath.FromSlash(f.path))
						if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
							t.Fatal(err)
						}
						if tc.differ && f.path == project.FlagsFilePath {
							f.data = []byte("different")
						}
						if err := os.WriteFile(p, f.data, 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			got := runCheck(context.Background(), &doctor.Context{Root: root, Deps: map[string]any{"now": time.Date(2026, 11, 6, 23, 0, 0, 0, time.UTC)}})
			if got.Status != tc.status || got.Message != tc.message || got.Try != tc.hint {
				t.Fatalf("result = %+v, want status %s message %q try %q", got, tc.status, tc.message, tc.hint)
			}
		})
	}
}
