package modules

// BillingNextSteps returns the manual billing wiring for an existing app.
func BillingNextSteps() string {
	return `Next: declare plans in spinup.toml, for example:
 [billing]
 default_plan = "free"
 [[billing.plans]]
 name = "free"
 features = ["basic"]
 [[billing.plans]]
 name = "pro"
 prices = ["pro_monthly", "pro_yearly"]
 features = ["basic", "export"]
 # Optional: apikeys_plan = "pro", role = "pro", meter = "api_calls".
 # Declare matching [apikeys] plans and [auth] roles before using these options.
Next: run spinup g billing.
Next: in api/cmd/api/main.go and api/cmd/worker/main.go, import:
 appbilling "<module>/api/internal/billing"
 "<module>/api/internal/roles"
 "github.com/stopyellingatme/spinup/kit/x/billing/stripe"
 kitbilling "github.com/stopyellingatme/spinup/kit/x/billing"
 "github.com/stopyellingatme/spinup/kit/config"
 "github.com/stopyellingatme/spinup/kit/jobs"
 "log/slog"
Next: in main.go, load the Stripe secrets with kit/config:
 var billingEnv struct {
  SecretKey string ` + "`env:\"STRIPE_SECRET_KEY\" required:\"true\"`" + `
  WebhookSecret string ` + "`env:\"STRIPE_WEBHOOK_SECRET\" required:\"true\"`" + `
  APIBase string ` + "`env:\"STRIPE_API_BASE\"`" + `
 }
 if err := config.Load(&billingEnv); err != nil { return err }
 var stripeOptions []stripe.Option
 if billingEnv.APIBase != "" { stripeOptions = append(stripeOptions, stripe.WithBaseURL(billingEnv.APIBase)) }
 stripeClient, err := stripe.New(billingEnv.SecretKey, stripeOptions...)
 if err != nil { return err }
 jobsClient, err := jobs.New(pool)
 if err != nil { return err }
Next: after web.PublicOrigin and keyStore exist, construct the service:
 billingSvc, err := kitbilling.New(pool, appbilling.Config, stripeClient,
  kitbilling.WithJobs(jobsClient),
  kitbilling.WithRoles(roles.Set),
  kitbilling.WithPublicOrigin(web.PublicOrigin),
  kitbilling.WithWebhookSecrets(billingEnv.WebhookSecret),
  kitbilling.WithLogger(slog.Default()),
  // With the apikeys module: kitbilling.WithAPIKeys(keyStore),
 )
 if err != nil { return err }
Next: with the apikeys module, add this option to apikeys.NewStore:
 apikeys.WithSessionOnly(kitbilling.SessionPrefix)
Next: add these options to app.New:
 app.WithRoutes(billingSvc.Routes(authn)),
 app.WithCommand("billing", billingSvc.Command()),
Next: in api/cmd/worker/main.go, add this code before returning w from newWorker:
 var billingEnv struct {
  SecretKey string ` + "`" + `env:"STRIPE_SECRET_KEY" required:"true"` + "`" + `
  WebhookSecret string ` + "`" + `env:"STRIPE_WEBHOOK_SECRET"` + "`" + `
  APIBase string ` + "`" + `env:"STRIPE_API_BASE"` + "`" + `
  PublicOrigin string ` + "`" + `env:"PUBLIC_ORIGIN" default:"http://localhost:5173"` + "`" + `
 }
 if err := config.Load(&billingEnv); err != nil { return nil, err }
 var stripeOptions []stripe.Option
 if billingEnv.APIBase != "" { stripeOptions = append(stripeOptions, stripe.WithBaseURL(billingEnv.APIBase)) }
 stripeClient, err := stripe.New(billingEnv.SecretKey, stripeOptions...)
 if err != nil { return nil, err }
 jobsClient, err := jobs.New(pool, jobs.WithLogger(logger))
 if err != nil { return nil, err }
 // With the apikeys module, also add these lines:
 // keyStore, err := apikeys.NewStore(pool, appapikeys.Config, apikeys.WithSessionOnly(kitbilling.SessionPrefix))
 // if err != nil { return nil, err }
 billingSvc, err := kitbilling.New(pool, appbilling.Config, stripeClient,
  kitbilling.WithJobs(jobsClient),
  kitbilling.WithRoles(roles.Set),
  kitbilling.WithPublicOrigin(billingEnv.PublicOrigin),
  kitbilling.WithWebhookSecrets(billingEnv.WebhookSecret),
  kitbilling.WithLogger(logger),
 // With the apikeys module, also add these lines:
  // kitbilling.WithAPIKeys(keyStore),
 )
 if err != nil { return nil, err }
 billingSvc.Register(w)
Next: with apikeys, enable the commented worker lines and import:
 appapikeys "<module>/api/internal/apikeys"
 "github.com/stopyellingatme/spinup/kit/x/apikeys"
Next: set the secrets for each deployment environment:
 spinup secrets set STRIPE_SECRET_KEY --env production < stripe-key.txt
 spinup secrets set STRIPE_WEBHOOK_SECRET --env production < stripe-webhook.txt
 Use a comma list of webhook secrets during rotation.
Next: in the Stripe Dashboard, create the products for your plans.
Next: in the Stripe Dashboard, create recurring prices with the declared lookup keys.
Next: in the Stripe Dashboard, create the api_calls meter when you enable API key metering.
Next: in the Stripe Dashboard, add /api/billing/webhook at API version 2026-10-28.endive.
 Select checkout.session.completed and checkout.session.async_payment_succeeded.
 Select customer.subscription.created, updated, deleted, paused, and resumed events.
 Select invoice.paid and invoice.payment_failed.
Next: in the Stripe Dashboard, enable and configure the Customer Portal.
Next: for local work, run stripe listen --forward-to localhost:5173/api/billing/webhook.
Next: link the user menu to /settings/billing and the public navigation to /pricing.
Next: load GET /api/me/billing in the root layout for signed-in users.
Next: run spinup db migrate, then api billing check.
Next: run spinup doctor and inspect billing-drift and billing-wired.
`
}
