package billingwired

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func golden(t *testing.T, parts ...string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{testutil.RepoRoot(t), "testdata", "golden", "billing"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGoldenWiring(t *testing.T) {
	for _, tc := range []struct {
		dir      string
		withKeys bool
	}{{"", false}, {"apikeys", true}} {
		api := golden(t, tc.dir, "api", "cmd", "api", "main.go")
		if got := checkAPI(api, tc.withKeys); got.Status != doctor.OK {
			t.Fatalf("%s api: %+v", tc.dir, got)
		}
		worker := golden(t, tc.dir, "api", "cmd", "worker", "main.go")
		if got := checkWorker(worker, tc.withKeys); got.Status != doctor.OK {
			t.Fatalf("%s worker: %+v", tc.dir, got)
		}
		if got := checkCompose(golden(t, tc.dir, "deploy", "compose.yml")); got.Status != doctor.OK {
			t.Fatalf("%s compose: %+v", tc.dir, got)
		}
	}
	// Without the apikeys module, the plain tree needs no apikeys lines.
	if got := checkAPI(golden(t, "api", "cmd", "api", "main.go"), true); got.Status != doctor.Warn || !strings.Contains(got.Try, "WithAPIKeys") {
		t.Fatalf("%+v", got)
	}
}

func TestAPIMissingLines(t *testing.T) {
	source := string(golden(t, "apikeys", "api", "cmd", "api", "main.go"))
	for _, tc := range []struct{ remove, try string }{
		{"billingSvc, err := kitbilling.New(", "billingSvc, err := kitbilling.New("},
		{"kitbilling.WithJobs(jobsClient),", "kitbilling.WithJobs(jobsClient) as an option"},
		{"kitbilling.WithWebhookSecrets(billingEnv.WebhookSecret),", "kitbilling.WithWebhookSecrets(billingEnv.WebhookSecret)"},
		{"kitbilling.WithPublicOrigin(web.PublicOrigin),", "kitbilling.WithPublicOrigin(web.PublicOrigin)"},
		{"kitbilling.WithAPIKeys(keyStore),", "kitbilling.WithAPIKeys(keyStore)"},
		{", apikeys.WithSessionOnly(kitbilling.SessionPrefix)", "apikeys.WithSessionOnly(kitbilling.SessionPrefix)"},
		{"app.WithRoutes(billingSvc.Routes(authn)),", "app.WithRoutes(billingSvc.Routes(authn)),"},
		{`app.WithCommand("billing", billingSvc.Command()),`, `app.WithCommand("billing", billingSvc.Command()),`},
	} {
		t.Run(tc.remove, func(t *testing.T) {
			if !strings.Contains(source, tc.remove) {
				t.Fatalf("the golden file has no %q", tc.remove)
			}
			replacement := ""
			if strings.HasSuffix(tc.remove, "New(") {
				replacement = "billingSvc, err := other.New("
			}
			got := checkAPI([]byte(strings.Replace(source, tc.remove, replacement, 1)), true)
			if got.Status != doctor.Warn || !strings.Contains(got.Try, tc.try) || !strings.HasPrefix(got.Message, apiPath+" lacks ") {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestWorkerMissingLines(t *testing.T) {
	source := string(golden(t, "apikeys", "api", "cmd", "worker", "main.go"))
	for _, tc := range []struct{ remove, try string }{
		{"billingSvc.Register(w)", "billingSvc.Register(w)"},
		{"kitbilling.WithAPIKeys(keyStore),", "kitbilling.WithAPIKeys(keyStore)"},
	} {
		got := checkWorker([]byte(strings.Replace(source, tc.remove, "", 1)), true)
		if got.Status != doctor.Warn || !strings.Contains(got.Try, tc.try) || !strings.HasPrefix(got.Message, workerPath+" lacks ") {
			t.Fatalf("%s: %+v", tc.remove, got)
		}
	}
}

func TestImportAlias(t *testing.T) {
	source := `package main
import (
	"github.com/stopyellingatme/spinup/kit/x/billing"
	kapp "github.com/stopyellingatme/spinup/kit/app"
)
func main() {
	svc, _ := billing.New(pool, cfg, client, billing.WithJobs(j), billing.WithWebhookSecrets(s), billing.WithPublicOrigin(o))
	kapp.New(kapp.WithRoutes(svc.Routes(a)), kapp.WithCommand("billing", svc.Command()))
}`
	if got := checkAPI([]byte(source), false); got.Status != doctor.OK {
		t.Fatalf("%+v", got)
	}
	if got := checkAPI([]byte(strings.Replace(source, `"billing", svc`, `"bill", svc`, 1)), false); got.Status != doctor.Warn || !strings.Contains(got.Message, `"billing" command`) {
		t.Fatalf("%+v", got)
	}
	if got := checkAPI([]byte("package main\nfunc {"), false); got.Status != doctor.Warn || !strings.Contains(got.Message, "cannot parse") {
		t.Fatalf("%+v", got)
	}
}

func TestCompose(t *testing.T) {
	list := "services:\n  api:\n    environment:\n      - STRIPE_SECRET_KEY_FILE=/run/secrets/STRIPE_SECRET_KEY\n      - STRIPE_WEBHOOK_SECRET_FILE=/run/secrets/STRIPE_WEBHOOK_SECRET\n  worker:\n    environment:\n      STRIPE_SECRET_KEY_FILE: /run/secrets/STRIPE_SECRET_KEY\n"
	got := checkCompose([]byte(list))
	if got.Status != doctor.Warn || got.Message != composePath+" lacks STRIPE_WEBHOOK_SECRET_FILE in the worker service." ||
		!strings.Contains(got.Try, "add STRIPE_WEBHOOK_SECRET_FILE: /run/secrets/STRIPE_WEBHOOK_SECRET to services.worker.environment") {
		t.Fatalf("%+v", got)
	}
	if got := checkCompose([]byte(list + "      STRIPE_WEBHOOK_SECRET_FILE: x\n")); got.Status != doctor.OK {
		t.Fatalf("%+v", got)
	}
	if got := checkCompose([]byte("services: [")); got.Status != doctor.Warn {
		t.Fatalf("%+v", got)
	}
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTriggers(t *testing.T) {
	root := t.TempDir()
	got := checkTriggers(root)
	if got.Status != doctor.Warn || !strings.Contains(got.Try, "spinup db new audit_billing") || strings.Count(got.Try, "CREATE TRIGGER") != 6 ||
		!strings.Contains(got.Try, "ON public.billing_subscriptions FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id');") {
		t.Fatalf("%+v", got)
	}
	// The billing migration alone: its DO block counts only after the audit functions exist.
	billing := string(golden(t, "api", "db", "kit", "00010_billing.sql"))
	write(t, root, "api/db/kit/00010_billing.sql", billing)
	if got := checkTriggers(root); got.Status != doctor.Warn {
		t.Fatalf("%+v", got)
	}
	write(t, root, "api/db/kit/00006_audit_events.sql", "-- +goose Up\nCREATE OR REPLACE FUNCTION audit_row_change() RETURNS trigger AS $$ BEGIN RETURN NULL; END $$ LANGUAGE plpgsql;\n")
	if got := checkTriggers(root); got.Status != doctor.OK {
		t.Fatalf("%+v", got)
	}
}

func TestRunCheckSkips(t *testing.T) {
	if got := runCheck(context.Background(), &doctor.Context{}); got.Status != doctor.Skip {
		t.Fatalf("%+v", got)
	}
	if got := runCheck(context.Background(), &doctor.Context{Root: t.TempDir()}); got.Status != doctor.Skip {
		t.Fatalf("%+v", got)
	}
}
