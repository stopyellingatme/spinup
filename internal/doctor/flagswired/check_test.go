package flagswired

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

const wired = `package main
import (
 ff "github.com/stopyellingatme/spinup/kit/x/flags"
 auth "github.com/stopyellingatme/spinup/kit/auth"
 app "github.com/stopyellingatme/spinup/kit/app"
)
func main() {
 login, err := auth.New(pool)
 provider, err := ff.New(ff.WithDeclared(appflags.Set))
 store := ff.NewStore(pool, provider, roles.Set)
 if err := store.Start(ctx, refresh); err != nil { return }
 a := app.New(
 app.WithMiddleware(login.Middleware()),
 app.WithMiddleware(provider.Middleware()),
 app.WithRoutes(store.Routes(login)),
 app.WithCommand("flags", store.Command()),
 app.WithShutdown(store.Close),
 )
}
`

func TestWiring(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, old, replacement, want string }{
		{name: "complete"},
		{"provider", "provider, err := ff.New(ff.WithDeclared(appflags.Set))", "provider := other.New()", "flags.New"},
		{"declarations", "ff.WithDeclared(appflags.Set)", "other.WithDeclared(appflags.Set)", "WithDeclared"},
		{"store", "store := ff.NewStore(pool, provider, roles.Set)", "store := other.NewStore()", "NewStore"},
		{"start", "store.Start(ctx, refresh)", "other.Start(ctx, refresh)", "Start"},
		{"middleware", "app.WithMiddleware(provider.Middleware()),", "", "provider.Middleware"},
		{"auth missing", "app.WithMiddleware(login.Middleware()),", "", "login.Middleware"},
		{"order", "app.WithMiddleware(login.Middleware()),\n app.WithMiddleware(provider.Middleware()),", "app.WithMiddleware(provider.Middleware()),\n app.WithMiddleware(login.Middleware()),", "before the auth middleware"},
		{"routes", "app.WithRoutes(store.Routes(login)),", "", "Routes"},
		{"command", "app.WithCommand(\"flags\", store.Command()),", "app.WithCommand(\"other\", store.Command()),", "WithCommand"},
		{"wrong command", "store.Command()", "other.Command()", "WithCommand"},
		{"shutdown", "app.WithShutdown(store.Close),", "", "Close"},
		{"comments", "app.WithMiddleware(provider.Middleware()),", "// app.WithMiddleware(provider.Middleware()),", "provider.Middleware"},
		{"outside app", "app.WithMiddleware(provider.Middleware()),", "other.WithMiddleware(provider.Middleware()),", "provider.Middleware"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := wired
			if tc.old != "" {
				source = strings.Replace(source, tc.old, tc.replacement, 1)
			}
			got := checkWiring([]byte(source))
			if tc.want == "" {
				if got.Status != doctor.OK {
					t.Fatalf("result = %+v", got)
				}
			} else if got.Status != doctor.Warn || !strings.Contains(got.Message, tc.want) || got.Try == "" {
				t.Fatalf("result = %+v", got)
			}
		})
	}
	if got := checkWiring([]byte("invalid")); got.Status != doctor.Warn {
		t.Fatal(got)
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	if got := runCheck(t.Context(), &doctor.Context{}); got.Status != doctor.Skip {
		t.Fatal(got)
	}
	root := t.TempDir()
	if got := runCheck(t.Context(), &doctor.Context{Root: root}); got.Status != doctor.Skip {
		t.Fatal(got)
	}
	lock := manifest.New()
	lock.Packs = []string{"api"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(t.Context(), &doctor.Context{Root: root}); got.Status != doctor.Skip {
		t.Fatal(got)
	}
	lock.Packs = append(lock.Packs, "flags")
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(t.Context(), &doctor.Context{Root: root}); got.Status != doctor.Warn {
		t.Fatal(got)
	}
	write(t, root, "api/cmd/api/main.go", wired)
	if got := runCheck(t.Context(), &doctor.Context{Root: root}); got.Status != doctor.OK {
		t.Fatal(got)
	}
	for _, c := range doctor.Checks(doctor.Local, true) {
		if c.ID == "flags-wired" {
			return
		}
	}
	t.Fatal("flags-wired is not registered")
}

func TestAuditMigrationOrder(t *testing.T) {
	t.Parallel()
	flagsSQL, err := os.ReadFile("../../templates/packs/flags/files/api/db/kit/00008_feature_flags.sql")
	if err != nil {
		t.Fatal(err)
	}
	auditSQL, err := os.ReadFile("../../templates/packs/audit/files/api/db/kit/00006_audit_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	const attach = `CREATE TRIGGER flags_audit AFTER INSERT OR UPDATE OR DELETE ON "public"."feature_flags" FOR EACH ROW EXECUTE FUNCTION "public"."audit_row_change"();`
	for _, tc := range []struct {
		name, auditPath, retrofit string
		want                      doctor.Status
	}{
		{"normal", "api/db/kit/00006_audit.sql", "", doctor.OK},
		{"late", "api/db/kit/00009_audit.sql", "", doctor.Warn},
		{"app audit", "api/db/migrations/00001_audit.sql", "", doctor.Warn},
		{"retrofit", "api/db/kit/00009_audit.sql", attach, doctor.OK},
		{"down ignored", "api/db/kit/00009_audit.sql", "-- +goose Down\n" + attach, doctor.Warn},
		{"comment ignored", "api/db/kit/00009_audit.sql", "/* " + attach + " */", doctor.Warn},
		{"conditional ignored", "api/db/kit/00009_audit.sql", "DO $$ BEGIN IF to_regprocedure('public.audit_row_change()') IS NOT NULL THEN " + attach + " END IF; END $$;", doctor.Warn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			lock := manifest.New()
			lock.Packs = []string{"flags", "audit"}
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			write(t, root, "api/cmd/api/main.go", wired)
			write(t, root, "api/db/kit/00008_flags.sql", string(flagsSQL))
			write(t, root, tc.auditPath, string(auditSQL))
			if tc.retrofit != "" {
				write(t, root, "api/db/migrations/00002_attach.sql", tc.retrofit)
			}
			got := runCheck(t.Context(), &doctor.Context{Root: root})
			if got.Status != tc.want {
				t.Fatalf("result=%+v", got)
			}
			if got.Status == doctor.Warn && (!strings.Contains(got.Message, "feature_flags can lack its audit triggers") || !strings.Contains(got.Try, "spinup db new audit_feature_flags")) {
				t.Fatal(got)
			}
		})
	}
}

func write(t *testing.T, root, path, text string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}
