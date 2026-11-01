package templates_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/doctor"
	_ "github.com/stopyellingatme/spinup/internal/doctor/auditwired"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenAudit(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "audit")
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, []string{"base", "api", "web", "auth", "audit"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "auth,audit"})
		selected := fstest.MapFS{}
		for _, path := range []string{"api/cmd/api/main.go", "api/db/migrations/00006_audit_events.sql", "web/src/lib/components/app/history-list.svelte"} {
			data, err := fs.ReadFile(all, path)
			if err != nil {
				return nil, err
			}
			selected[path] = &fstest.MapFile{Data: data, Mode: 0o644}
		}
		return selected, nil
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "audit", fsys)
	testutil.Deterministic(t, render)
	// Exercise the rendered wiring and migration through the registered static check.
	root := t.TempDir()
	for _, path := range []string{"api/cmd/api/main.go", "api/db/migrations/00006_audit_events.sql"} {
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Check the real conditional trigger against the OIDC table migration too.
	identityMigration, err := fs.ReadFile(templates.Embedded(), "auth/files/api/db/migrations/00004_oidc_identities.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api/db/migrations/00004_oidc_identities.sql"), identityMigration, 0o600); err != nil {
		t.Fatal(err)
	}
	lock := manifest.New()
	lock.Packs = []string{"api", "auth", "audit"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	for _, check := range doctor.Checks(doctor.Local, true) {
		if check.ID == "audit-wired" {
			if got := check.Run(t.Context(), &doctor.Context{Root: root}); got.Status != doctor.OK {
				t.Fatalf("rendered audit wiring: %+v", got)
			}
			return
		}
	}
	t.Fatal("audit-wired is not registered")
}

func TestAuditOffKeepsMain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, modules string
		packs         []string
	}{
		{"api", "", []string{"base", "api"}},
		{"auth", "ci,auth", []string{"base", "api", "web", "auth"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rendered := testutil.RenderPack(t, tc.packs, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": tc.modules})
			got, err := fs.ReadFile(rendered, "api/cmd/api/main.go")
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "testdata/golden", tc.name, "api/cmd/api/main.go"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatal("audit-disabled main.go differs from the existing golden")
			}
		})
	}
	pack, err := templates.Load(templates.Embedded(), "audit")
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Files) != 2 {
		t.Fatalf("audit files = %v", pack.Files)
	}
}
