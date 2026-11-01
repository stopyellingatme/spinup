package auditwired

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
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
			write(t, root, "api/db/migrations/00004_oidc.sql", tc.migration)
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
