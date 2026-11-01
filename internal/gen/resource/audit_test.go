package resource

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func auditApp(t *testing.T) string {
	t.Helper()
	root := freshApp(t)
	writeLock(t, root, []string{"base", "api", "auth", "audit"})
	write(t, root, map[string]string{
		"api/db/migrations/00006_audit_events.sql": "-- +goose Up\nCREATE TABLE audit_events (id bigint);\n-- +goose Down\nDROP TABLE audit_events;\n",
	})
	return root
}

func TestAuditFlags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                         string
		flags                        []string
		missingModule, missingSchema bool
		want                         string
		on                           bool
	}{
		{name: "default", on: true},
		{name: "disabled", flags: []string{"--no-audit"}},
		{name: "explicit", flags: []string{"--audit"}, on: true},
		{name: "missing module", flags: []string{"--audit"}, missingModule: true, want: "Try: spinup module add audit"},
		{name: "missing schema", missingSchema: true, want: "audit_events"},
		{name: "conflicting flags", flags: []string{"--audit", "--no-audit"}, want: "Try:"},
		{name: "unknown redact", flags: []string{"--audit-redact", "missing"}, want: "missing"},
		{name: "unknown history role", flags: []string{"--history-role", "missing"}, want: "missing"},
		{name: "redact disabled", flags: []string{"--no-audit", "--audit-redact", "title"}, want: "Try:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := auditApp(t)
			if tc.missingModule {
				writeLock(t, root, []string{"base", "api"})
			}
			if tc.missingSchema {
				if err := os.Remove(filepath.Join(root, "api/db/migrations/00006_audit_events.sql")); err != nil {
					t.Fatal(err)
				}
			}
			out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note", "title:string", "api_token:string"}, Flags: flagSet(t, append(tc.flags, "--api-only")...)})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v, want %s", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, f := range out.Plan.Files {
				if strings.HasSuffix(f.Path, "_history.go") {
					found = true
				}
				if tc.on && strings.Contains(f.Path, "create_notes.sql") && !strings.Contains(string(f.Data), "audit_row_change('user_id', 'id', 'api_token')") {
					t.Fatalf("missing trigger: %s", f.Data)
				}
			}
			if found != tc.on {
				t.Fatalf("history = %v, want %v", found, tc.on)
			}
		})
	}
}

func TestGoldenGenResourceAudit(t *testing.T) {
	t.Parallel()
	if !testutil.GoldenSelected(t, "gen-resource-audit") {
		return
	}
	root := renderApp(t, []string{"base", "api", "web", "auth", "audit"}, []string{"auth", "audit"})
	write(t, root, map[string]string{"web/src/lib/components/app/history-list.svelte": "<!-- Supplied by the audit pack. -->"})
	render := func() (fs.FS, error) {
		out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note", "title:string", "api_token:string"}, Flags: flagSet(t, "--role", "manager,staff", "--history-role", "manager")})
		if err != nil {
			return nil, err
		}
		got := fstest.MapFS{}
		for _, f := range out.Plan.Files {
			got[f.Path] = &fstest.MapFile{Data: f.Data, Mode: 0o644}
		}
		return got, nil
	}
	got, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "gen-resource-audit", got)
	testutil.Deterministic(t, render)
}

func TestAuditMigrationOrder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, migration string
		want            bool
	}{
		{"older", "00007_create_notes.sql", false},
		{"same", "00006_create_notes.sql", true},
		{"newer", "00005_create_notes.sql", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := CheckAuditMigration(auditApp(t), tc.migration)
			if (err != nil) != tc.want {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestAuditPageInputs(t *testing.T) {
	t.Parallel()
	root := authApp(t)
	if err := checkPageInputs(root, true); err == nil || !strings.Contains(err.Error(), "history-list.svelte") {
		t.Fatalf("error = %v", err)
	}
	write(t, root, map[string]string{"web/src/lib/components/app/history-list.svelte": "<!-- fixture -->"})
	if err := checkPageInputs(root, true); err != nil {
		t.Fatal(err)
	}
}

func TestAuditSchemaWrittenOnce(t *testing.T) {
	t.Parallel()
	root := auditApp(t)
	first, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note", "title:string"}, Flags: flagSet(t, "--api-only")})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range first.Plan.Files {
		write(t, root, map[string]string{f.Path: string(f.Data)})
	}
	second, err := run(t.Context(), gen.Input{Root: root, Args: []string{"item", "title:string"}, Flags: flagSet(t, "--api-only", "--shared")})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range second.Plan.Files {
		if f.Path == auditSchemasPath {
			t.Fatal("shared audit schemas were written twice")
		}
		if strings.Contains(f.Path, "_history.go") && strings.Contains(string(f.Data), "filter.OwnerID") {
			t.Fatal("shared history has an owner filter")
		}
	}
}

func TestAuditHistoryRoles(t *testing.T) {
	t.Parallel()
	root := renderApp(t, []string{"base", "api", "web", "auth", "audit"}, []string{"auth", "audit"})
	out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note", "title:string"}, Flags: flagSet(t, "--api-only", "--role", "manager")})
	if err != nil {
		t.Fatal(err)
	}
	f, ok := out.Plan.Get("api/internal/handlers/notes_history.go")
	if !ok || !strings.Contains(string(f.Data), "user.HasRole(roles.Manager)") {
		t.Fatal("history roles do not default to resource roles")
	}
}

func TestExistingResourceUsesRetrofit(t *testing.T) {
	t.Parallel()
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			t.Parallel()
			root := auditApp(t)
			write(t, root, map[string]string{"api/db/migrations/00007_create_notes.sql": "-- +goose Up\nCREATE TABLE notes (id bigint, title text);\n"})
			args := []string{"--api-only"}
			if force {
				args = append(args, "--force")
			}
			_, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note", "title:string"}, Flags: flagSet(t, args...)})
			if err == nil || !strings.Contains(err.Error(), "Try: spinup g audit notes") || strings.Contains(err.Error(), "--force") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
