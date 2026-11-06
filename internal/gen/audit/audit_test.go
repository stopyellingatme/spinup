package audit

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/spf13/pflag"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/resource"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func write(t *testing.T, root, path, data string) {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	app := testutil.RenderPack(t, []string{"base", "api", "web", "auth", "audit"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": []string{"auth", "audit"}})
	if err := os.CopyFS(root, app); err != nil {
		t.Fatal(err)
	}
	lock := manifest.New()
	lock.Packs = []string{"base", "api", "auth", "audit"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "api/db/migrations/00007_create_notes.sql", "-- +goose Up\nCREATE TABLE notes (id bigint PRIMARY KEY, user_id bigint NOT NULL, title text, api_token text);\n-- +goose Down\nDROP TABLE notes;\n")
	write(t, root, "api/internal/handlers/notes.go", `package handlers
func (s *Server) GetNote(ctx context.Context, req gen.GetNoteRequestObject) (gen.GetNoteResponseObject, error) {
 user, ok := auth.UserFrom(ctx)
 if !ok {return nil, nil}
 if !user.HasRole(roles.Manager) {return nil, nil}
 row, err := s.Queries.GetNote(ctx, store.GetNoteParams{ID:req.Id, UserID:user.ID})
 return row, err
}
`)
	return root
}

func flags(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	f := pflag.NewFlagSet("audit", pflag.ContinueOnError)
	f.Bool("api-only", false, "")
	f.String("audit-redact", "", "")
	f.String("history-role", "", "")
	if err := f.Parse(args); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestGoldenGenAudit(t *testing.T) {
	t.Parallel()
	if !testutil.GoldenSelected(t, "gen-audit") {
		return
	}
	root := fixture(t)
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lock.Packs = append(lock.Packs, "web")
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "web/src/lib/components/app/history-list.svelte", "<!-- Supplied by the audit pack. -->")
	render := func() (fs.FS, error) {
		out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note"}, Flags: flags(t)})
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
	testutil.Golden(t, "gen-audit", got)
	testutil.Deterministic(t, render)
}

// oldIdentities is auth 00004 as spinup wrote it before the migration attached
// the audit triggers itself (ADR 0025 decision 3.9).
const oldIdentities = `-- +goose Up
-- A new migration follows password reset. Existing auth migrations never change.
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

CREATE TABLE user_identities (
    provider text NOT NULL,
    subject text NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email text NOT NULL,
    PRIMARY KEY (provider, subject)
);
CREATE INDEX user_identities_user_id_idx ON user_identities (user_id);

-- +goose Down
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
DROP TABLE user_identities;
`

// retrofitIdentities is the attach that spinup g audit user_identities writes for
// an old 00004 copy. The audit migration may have attached the triggers already,
// so it drops them first.
const retrofitIdentities = `-- +goose Up
DROP TRIGGER IF EXISTS user_identities_audit_truncate ON user_identities;
DROP TRIGGER IF EXISTS user_identities_audit ON user_identities;
CREATE TRIGGER user_identities_audit AFTER INSERT OR UPDATE OR DELETE ON user_identities FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'provider,subject');
CREATE TRIGGER user_identities_audit_truncate AFTER TRUNCATE ON user_identities FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();

-- +goose Down
DROP TRIGGER IF EXISTS user_identities_audit_truncate ON user_identities;
DROP TRIGGER IF EXISTS user_identities_audit ON user_identities;
`

func newIdentities(t *testing.T) string {
	t.Helper()
	data, err := fs.ReadFile(templates.Embedded(), "auth/files/api/db/kit/00004_oidc_identities.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRetrofit(t *testing.T) {
	t.Parallel()
	const identities = "00004_oidc_identities.sql"
	for _, tc := range []struct {
		name, target, file, sql, extra, want string
		args                                 []string
		onlyMigration, idempotent            bool
	}{
		{name: "resource", target: "notes"},
		{name: "already attached", target: "note", sql: "CREATE TRIGGER notes_audit AFTER INSERT ON notes FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id');", want: "already attaches"},
		{name: "truncate already attached", target: "note", sql: "CREATE TRIGGER notes_audit_truncate AFTER TRUNCATE ON notes FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();", want: "already attaches"},
		{name: "comment ignored", target: "note", sql: "-- CREATE TRIGGER notes_audit AFTER INSERT ON notes FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id');"},
		{name: "down ignored", target: "note", sql: "-- +goose Down\nCREATE TRIGGER notes_audit AFTER INSERT ON notes FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id');"},
		{name: "unknown table", target: "absent", want: "no migration creates"},
		{name: "unknown redact", target: "notes", args: []string{"--audit-redact", "absent"}, want: "no such column"},
		{name: "old oidc migration", target: "user_identities", file: identities, sql: oldIdentities, onlyMigration: true, idempotent: true},
		{name: "oidc migration attaches", target: "user_identities", file: identities, sql: newIdentities(t), want: "already attaches"},
		{name: "old oidc migration retrofitted", target: "user_identities", file: identities, sql: oldIdentities, extra: retrofitIdentities, want: "already attaches"},
		{name: "auth users attached", target: "users", want: "already attaches"},
		{name: "secret table", target: "sessions", want: "authentication secrets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := fixture(t)
			if tc.file != "" {
				write(t, root, "api/db/kit/"+tc.file, tc.sql)
			} else if tc.sql != "" {
				write(t, root, "api/db/migrations/00008_test.sql", "-- +goose Up\n"+tc.sql)
			}
			if tc.extra != "" {
				write(t, root, "api/db/migrations/00009_audit_user_identities.sql", tc.extra)
			}
			out, err := run(t.Context(), gen.Input{Root: root, Args: []string{tc.target}, Flags: flags(t, tc.args...)})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("error = %v, want %s", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.onlyMigration && len(out.Plan.Files) != 1 {
				t.Fatalf("files = %+v", out.Plan.Files)
			}
			for _, f := range out.Plan.Files {
				if f.Path == "api/internal/handlers/notes.go" || strings.HasSuffix(f.Path, "[id]/+page.svelte") {
					t.Fatalf("retrofit edits %s", f.Path)
				}
				if tc.onlyMigration && !strings.Contains(string(f.Data), "audit_row_change('', 'provider,subject')") {
					t.Fatalf("wrong auth arguments: %s", f.Data)
				}
				if tc.idempotent && string(f.Data) != retrofitIdentities {
					t.Fatalf("attach migration =\n%s\nwant\n%s", f.Data, retrofitIdentities)
				}
			}
			if !tc.onlyMigration && !strings.Contains(strings.Join(out.Next, "\n"), "audit.InTx") {
				t.Fatalf("missing recipe: %v", out.Next)
			}
		})
	}
}

func TestConditionalAttachAcrossSpaces(t *testing.T) {
	t.Parallel()
	root := fixture(t)
	write(t, root, "api/db/migrations/00002_create_widgets.sql", "-- +goose Up\nCREATE TABLE widgets(id bigint);")
	write(t, root, "api/db/kit/00009_conditional.sql", `-- +goose Up
DO $$ BEGIN
IF to_regclass('public.widgets') IS NOT NULL THEN
CREATE TRIGGER widgets_audit AFTER INSERT ON widgets FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'id');
END IF;
END $$;`)
	file, err := planMigration(resource.Spec{Root: root}, "widgets", "id")
	if err != nil {
		t.Fatal(err)
	}
	if file.Path != "api/db/migrations/00008_audit_widgets.sql" || !strings.Contains(string(file.Data), "DROP TRIGGER IF EXISTS widgets_audit") {
		t.Fatalf("migration = %+v", file)
	}
}
