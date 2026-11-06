package auditwired

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func TestCheck(t *testing.T) {
	t.Parallel()
	const wired = `package main
func main() { auth.WithEvents(authsink.New(pool)); auth.WithActorContext(authsink.ActorContext); app.WithCommand("audit", audit.Command(pool)) }
`
	for _, tc := range []struct {
		name, main, migration, want string
		packs                       []string
		status                      doctor.Status
	}{
		{name: "outside", status: doctor.Skip},
		{name: "disabled", packs: []string{"api"}, status: doctor.Skip},
		{name: "wired", packs: []string{"audit"}, main: wired, status: doctor.OK},
		{name: "missing main", packs: []string{"audit"}, status: doctor.Warn, want: "main.go"},
		{name: "missing command", packs: []string{"audit"}, main: `package main; func main() { authsink.New(pool) }`, status: doctor.Warn, want: "audit.Command"},
		{name: "missing sink", packs: []string{"audit"}, main: `package main; func main() { audit.Command(pool) }`, status: doctor.Warn, want: "authsink"},
		{name: "comments are not wiring", packs: []string{"audit"}, main: "package main\n// audit.Command(pool), authsink.New(pool)\n", status: doctor.Warn, want: "audit.Command"},
		{name: "identities missing trigger", packs: []string{"audit"}, main: wired, migration: "CREATE TABLE public.user_identities (id bigint);", status: doctor.Warn, want: "user_identities"},
		{name: "identities audited", packs: []string{"audit"}, main: wired, migration: "CREATE TABLE user_identities (id bigint);\nCREATE TRIGGER user_identities_audit AFTER INSERT OR UPDATE OR DELETE ON public.user_identities FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'provider,subject');", status: doctor.OK},
		{name: "quoted identifiers", packs: []string{"audit"}, main: wired, migration: `CREATE TABLE IF NOT EXISTS "public"."user_identities" (id bigint); CREATE TRIGGER "user_identities_audit" AFTER INSERT ON "public"."user_identities" FOR EACH ROW EXECUTE FUNCTION "public"."audit_row_change"('', 'provider,subject');`, status: doctor.OK},
		{name: "unrelated trigger", packs: []string{"audit"}, main: wired, migration: "CREATE TABLE user_identities (id bigint); CREATE TRIGGER identities_touch BEFORE UPDATE ON user_identities FOR EACH ROW EXECUTE FUNCTION touch();", status: doctor.Warn, want: "user_identities"},
		{name: "down trigger ignored", packs: []string{"audit"}, main: wired, migration: "-- +goose Up\nCREATE TABLE user_identities (id bigint);\n-- +goose Down\nCREATE TRIGGER identities_audit AFTER INSERT ON user_identities FOR EACH ROW EXECUTE FUNCTION audit_row_change();", status: doctor.Warn, want: "user_identities"},
		{name: "commented table ignored", packs: []string{"audit"}, main: wired, migration: "-- CREATE TABLE user_identities (id bigint);", status: doctor.OK},
		{name: "commented trigger ignored", packs: []string{"audit"}, main: wired, migration: "CREATE TABLE user_identities (id bigint); /* CREATE TRIGGER identities_audit AFTER INSERT ON user_identities FOR EACH ROW EXECUTE FUNCTION audit_row_change(); */", status: doctor.Warn, want: "user_identities"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tc.packs != nil {
				lock := manifest.New()
				lock.Packs = tc.packs
				if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.main != "" {
				write(t, root, "api/cmd/api/main.go", tc.main)
			}
			write(t, root, "api/db/migrations/00004_oidc_identities.sql", tc.migration)
			got := runCheck(t.Context(), &doctor.Context{Root: root})
			if got.Status != tc.status || !strings.Contains(got.Message, tc.want) {
				t.Fatalf("result = %+v", got)
			}
			if got.Status == doctor.Warn && got.Try == "" {
				t.Fatal("warning has no Try line")
			}
		})
	}
}

// oldIdentities is auth 00004 as spinup wrote it before the migration attached
// the audit triggers itself (ADR 0025 decision 3.9).
const oldIdentities = `-- +goose Up
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

CREATE TABLE user_identities (
    provider text NOT NULL,
    subject text NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email text NOT NULL,
    PRIMARY KEY (provider, subject)
);

-- +goose Down
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
DROP TABLE user_identities;
`

// retrofit is the attach migration of spinup g audit user_identities.
const retrofit = `-- +goose Up
DROP TRIGGER IF EXISTS user_identities_audit_truncate ON user_identities;
DROP TRIGGER IF EXISTS user_identities_audit ON user_identities;
CREATE TRIGGER user_identities_audit AFTER INSERT OR UPDATE OR DELETE ON user_identities FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'provider,subject');
CREATE TRIGGER user_identities_audit_truncate AFTER TRUNCATE ON user_identities FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();

-- +goose Down
DROP TRIGGER IF EXISTS user_identities_audit_truncate ON user_identities;
DROP TRIGGER IF EXISTS user_identities_audit ON user_identities;
`

// TestIdentityOrder uses the real pack migrations 00004 and 00006. The kit
// migrator can apply 00004 after 00006 when OIDC comes later.
func TestIdentityOrder(t *testing.T) {
	t.Parallel()
	const wired = `package main
func main() { auth.WithEvents(authsink.New(pool)); auth.WithActorContext(authsink.ActorContext); app.WithCommand("audit", audit.Command(pool)) }
`
	pack := func(path string) string {
		data, err := fs.ReadFile(templates.Embedded(), path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	audit := pack("audit/files/api/db/kit/00006_audit_events.sql")
	identities := pack("auth/files/api/db/kit/00004_oidc_identities.sql")
	for _, tc := range []struct {
		name   string
		files  map[string]string
		status doctor.Status
	}{
		{name: "no oidc", files: map[string]string{"00006_audit_events.sql": audit}, status: doctor.OK},
		{name: "oidc migration attaches", files: map[string]string{"00004_oidc_identities.sql": identities, "00006_audit_events.sql": audit}, status: doctor.OK},
		{name: "old oidc migration", files: map[string]string{"00004_oidc_identities.sql": oldIdentities, "00006_audit_events.sql": audit}, status: doctor.Warn},
		{name: "old oidc migration retrofitted", files: map[string]string{
			"00004_oidc_identities.sql": oldIdentities, "00006_audit_events.sql": audit, "00009_audit_user_identities.sql": retrofit,
		}, status: doctor.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			lock := manifest.New()
			lock.Packs = []string{"api", "auth", "audit"}
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			write(t, root, "api/cmd/api/main.go", wired)
			if err := os.MkdirAll(filepath.Join(root, "api/db/migrations"), 0o755); err != nil {
				t.Fatal(err)
			}
			for name, data := range tc.files {
				dir := "api/db/kit/"
				if strings.Contains(name, "audit_user_identities") {
					dir = "api/db/migrations/"
				}
				write(t, root, dir+name, data)
			}
			got := runCheck(t.Context(), &doctor.Context{Root: root})
			if got.Status != tc.status {
				t.Fatalf("result = %+v", got)
			}
			if got.Status == doctor.Warn && (!strings.Contains(got.Message, "user_identities") || !strings.Contains(got.Try, "spinup g audit user_identities")) {
				t.Fatalf("warning = %+v", got)
			}
		})
	}
}

func TestRegistered(t *testing.T) {
	t.Parallel()
	for _, check := range doctor.Checks(doctor.Local, true) {
		if check.ID == "audit-wired" {
			return
		}
	}
	t.Fatal("audit-wired is not registered")
}

func write(t *testing.T, root, path, data string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
