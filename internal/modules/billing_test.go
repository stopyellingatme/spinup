package modules

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func TestSelectBilling(t *testing.T) {
	t.Parallel()
	lock := manifest.New()
	lock.Packs = []string{"base", "api", "web"}
	lock.Answers = map[string]any{"app_name": "demo", "module": "example.com/demo", "db_version": "18"}
	got, err := Select(t.Context(), SelectOptions{Config: &project.Config{}, Lock: lock, FS: templates.Embedded(), Names: []string{"billing"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Added, ",") != "auth,billing,jobs" || strings.Join(got.Required, ",") != "auth,jobs" {
		t.Fatalf("selection=%+v", got)
	}
	lock.Packs = []string{"base", "api"}
	if _, err := Select(t.Context(), SelectOptions{Config: &project.Config{}, Lock: lock, FS: templates.Embedded(), Names: []string{"billing"}}); err == nil || !strings.Contains(err.Error(), "API-only") {
		t.Fatalf("error=%v", err)
	}
}

func TestBillingNextSteps(t *testing.T) {
	for _, want := range []string{
		"[billing]", "[[billing.plans]]", "spinup g billing", "stripe.New", "stripe.WithBaseURL",
		"kitbilling.New", "kitbilling.WithJobs", "kitbilling.WithRoles(roles.Set)",
		"kitbilling.WithPublicOrigin", "kitbilling.WithWebhookSecrets", "kitbilling.WithLogger",
		"kitbilling.WithAPIKeys(keyStore)", "apikeys.WithSessionOnly(kitbilling.SessionPrefix)",
		"app.WithRoutes(billingSvc.Routes(authn))", `app.WithCommand("billing", billingSvc.Command())`,
		"billingSvc.Register(w)", "spinup secrets set STRIPE_SECRET_KEY", "spinup secrets set STRIPE_WEBHOOK_SECRET",
		"Stripe Dashboard", "2026-10-28.endive", "Customer Portal", "stripe listen --forward-to localhost:5173/api/billing/webhook",
		"spinup db migrate", "billing-drift", "billing-wired",
	} {
		if !strings.Contains(BillingNextSteps(), want) {
			t.Errorf("missing %q", want)
		}
	}
}
