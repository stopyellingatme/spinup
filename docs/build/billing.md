---
title: Billing with Stripe
parent: Build your app
nav_order: 20
---

# Billing with Stripe

The `billing` module sells subscriptions with Stripe. A user picks a plan on the page `/pricing`, and pays on the Stripe Checkout page.
A plan opens features in the API and the web app. A plan can also set the [apikeys](api-keys.md) plan of the user and give the user a [role](roles.md).
With the apikeys module, the module sends the API key calls of each user to a Stripe meter, so you can bill for usage.
The module needs the `auth` and `jobs` modules.

```console
$ spinup new todo --module example.com/todo --modules auth,jobs,billing --no-git --non-interactive
...
$ cd todo
$ spinup doctor --quick 2>/dev/null | grep billing-
ok	billing-drift	the generated billing files match [billing] in spinup.toml
ok	billing-wired	billing wiring is present
```

## How billing works

The app never sees card data. Stripe hosts the Checkout page and the Customer Portal.

1. The user clicks Subscribe on `/pricing`. The API makes a Stripe customer for the user and a Checkout session.
2. The user pays on the Stripe page. Stripe sends the user back to `/settings/billing?session_id=...`.
3. Stripe sends a webhook event to `POST /api/billing/webhook`. The API checks the signature, records the event ID, and queues a sync job.
4. The worker runs the job. The job reads the subscriptions of the customer from Stripe and writes the plan of the user.
5. The page `/settings/billing` also runs the sync for its `session_id`. So the user sees the new plan, even when the webhook is late.

The sync always reads the current state from Stripe. It never trusts the event payload. Thus the order of the events does not matter, and a repeated event does nothing.
The billing entity is the user. Each user has zero or one Stripe customer.

## Enable the module

Add the module to a new app with `--modules auth,jobs,billing`, as the first example shows.
Add `apikeys` to the list to link plans to API key plans and to meter API calls.
To add the module to an existing app, upgrade the app and then add the module. The command makes a branch for you to review.

<!-- doctest: skip -->
```console
$ spinup upgrade --yes
$ spinup module add billing --yes --non-interactive
...
Next: git merge spinup/module-billing
```

The migration goes in `api/db/kit/00010_billing.sql`, so it does not clash with your own migrations.
The command prints `Next:` lines for the files that spinup does not overwrite: `api/cmd/api/main.go` and `api/cmd/worker/main.go`.
These are the main edits in `api/cmd/api/main.go`. Put this block after `auth.New`:

```go
var billingEnv struct {
	SecretKey     string `env:"STRIPE_SECRET_KEY" required:"true"`
	WebhookSecret string `env:"STRIPE_WEBHOOK_SECRET" required:"true"`
	APIBase       string `env:"STRIPE_API_BASE"`
}
if err := config.Load(&billingEnv); err != nil {
	return err
}
var stripeOptions []stripe.Option
if billingEnv.APIBase != "" {
	stripeOptions = append(stripeOptions, stripe.WithBaseURL(billingEnv.APIBase))
}
stripeClient, err := stripe.New(billingEnv.SecretKey, stripeOptions...)
if err != nil {
	return err
}
jobsClient, err := jobs.New(pool)
if err != nil {
	return err
}
billingSvc, err := kitbilling.New(pool, appbilling.Config, stripeClient,
	kitbilling.WithJobs(jobsClient),
	kitbilling.WithRoles(roles.Set),
	kitbilling.WithPublicOrigin(web.PublicOrigin),
	kitbilling.WithWebhookSecrets(billingEnv.WebhookSecret),
	kitbilling.WithLogger(slog.Default()),
	kitbilling.WithAPIKeys(keyStore), // only with the apikeys module
)
if err != nil {
	return err
}
```

Then add these options to `app.New`:

```go
app.WithRoutes(billingSvc.Routes(authn)),
app.WithCommand("billing", billingSvc.Command()),
```

With the apikeys module, also add `apikeys.WithSessionOnly(kitbilling.SessionPrefix)` to the options of `apikeys.NewStore`. An API key then cannot call the billing routes.
The worker builds the same service and calls `billingSvc.Register(w)` before `newWorker` returns. The `Next:` lines print the full worker block.
The production Compose file `deploy/compose.yml` gets the two secret files in the `api` and `worker` services. spinup manages that file, so the upgrade merges the change.
Run `spinup db migrate up` before you start the new binaries. The doctor check `billing-wired` finds a missing line.

## Declare plans

Plans are an array of tables in `spinup.toml`. The order is the rank: the first plan is the lowest.

```toml
[billing]
default_plan = "free"
trial_days = 0
automatic_tax = false
allow_promotion_codes = false

[[billing.plans]]
name = "free"
features = ["basic"]
apikeys_plan = "free"

[[billing.plans]]
name = "pro"
prices = ["pro_monthly", "pro_yearly"]
features = ["basic", "export"]
apikeys_plan = "pro"
role = "pro"
meter = "api_calls"
```

| Key | Default | What it does |
| --- | --- | --- |
| `default_plan` | `free` | The plan of a user with no live subscription. It has no prices, no role, and no meter. |
| `trial_days` | `0` | The trial of a new subscription, from 0 to 730 days. 0 means no trial. |
| `automatic_tax` | `false` | Turns on Stripe Tax in Checkout. |
| `allow_promotion_codes` | `false` | Shows the promotion code field in Checkout. |
| `plans[].name` | none | The plan name. It matches `^[a-z][a-z0-9_]{0,31}$`. |
| `plans[].prices` | none | 1 to 8 Stripe price lookup keys. A key is on one plan only. The default plan has none. |
| `plans[].features` | none | Up to 32 feature names. A feature matches `^[a-z][a-z0-9_]{0,31}$`. |
| `plans[].apikeys_plan` | none | A plan of `[apikeys]`. It needs the apikeys module. |
| `plans[].role` | none | A role of `[auth]`. It is not the admin role and not the default role. |
| `plans[].meter` | none | The `event_name` of a Stripe meter for the API key calls. It needs the apikeys module. |

Without a `[billing]` table, the app has one plan, `free`, with no features.
The app has 1 to 16 plans. An unknown key is an error.

Run `spinup g billing` after each change. The command writes `api/internal/billing/billing.gen.go` and `web/src/lib/billing.gen.ts`.
See [generate billing](../commands/generate-billing.md).

```console
$ printf '\n[billing]\ndefault_plan = "free"\n\n[[billing.plans]]\nname = "free"\nfeatures = ["basic"]\n\n[[billing.plans]]\nname = "pro"\nprices = ["pro_monthly", "pro_yearly"]\nfeatures = ["basic", "export"]\n' >> spinup.toml
$ spinup g billing
Created 0, updated 2 files
$ grep -c 'Plan[A-Z]' api/internal/billing/billing.gen.go
2
```

A bad declaration fails with the rule and a `Try:` line, and changes no file.

## Set up Stripe

Do these steps in the Stripe Dashboard. Do them first in test mode.

1. Make a product for each paid plan, for example "Pro".
2. Make a recurring price for each lookup key in `prices`. Set the lookup key of the price to the same value, for example `pro_monthly`.
3. With a `meter`, make a meter with that event name, for example `api_calls`. Add a metered price on that meter to the plan, and declare its lookup key in `prices`.
4. Add a webhook endpoint at `https://<your app>/api/billing/webhook` with API version `2026-10-28.endive`. Select these events:
   `checkout.session.completed`, `checkout.session.async_payment_succeeded`, `customer.subscription.created`, `customer.subscription.updated`,
   `customer.subscription.deleted`, `customer.subscription.paused`, `customer.subscription.resumed`, `invoice.paid`, and `invoice.payment_failed`.
5. Turn on the Customer Portal. Choose the plans and prices that a user can change to, and allow a cancel.

The API answers 200 to other event types and ignores them.
Then run the check command from the `api/` folder. It checks the key, each lookup key, each meter, and the webhook secret.

<!-- doctest: skip -->
```console
$ go run ./cmd/api billing check
ok    Stripe key
ok    price pro_monthly (price_1Q..., every 1 month, licensed)
ok    price pro_yearly (price_1Q..., every 1 year, licensed)
ok    webhook secret (1 set)
```

A line that starts with `FAIL` names the problem. The command exits with a non-zero status when a check fails.

## Secrets

The API and the worker read two secrets with `kit/config`. The `_FILE` form works, as for every secret.

| Variable | What it holds |
| --- | --- |
| `STRIPE_SECRET_KEY` | The secret key or a restricted key (`sk_...` or `rk_...`). |
| `STRIPE_WEBHOOK_SECRET` | The signing secret of the webhook endpoint (`whsec_...`). During a rotation, give a comma list of the old and the new secret. |
| `STRIPE_API_BASE` | Only for tests. It points the client at a fake Stripe API. |

The API does not start without the two secrets. Commands of the app binary, such as `spinup db migrate`, also need them in the environment.
For production, store them with [secrets](../deploy/secrets.md):

<!-- doctest: skip -->
```console
$ spinup secrets set STRIPE_SECRET_KEY --env production < stripe-key.txt
$ spinup secrets set STRIPE_WEBHOOK_SECRET --env production < stripe-webhook.txt
```

A Stripe error in a log or an answer has the Stripe request ID. It never has the key.

## Work on your computer

Use a test mode key. The Stripe CLI forwards the test events to the dev server, and prints the webhook secret to use.

<!-- doctest: skip -->
```console
$ stripe listen --forward-to localhost:5173/api/billing/webhook
> Ready! Your webhook signing secret is whsec_...
```

Put the key and the printed secret in `.env` as `STRIPE_SECRET_KEY` and `STRIPE_WEBHOOK_SECRET`, and run `spinup dev`.
The `jobs` worker must run, because the worker syncs the plan after each event.

This example makes an account and prints its billing state. It needs an empty database in `DATABASE_URL`. The key values are placeholders, because these commands do not call Stripe:

<!-- doctest: requires-env DATABASE_URL -->
```console
$ STRIPE_SECRET_KEY=sk_test_docs STRIPE_WEBHOOK_SECRET=whsec_docs spinup db migrate up
...
$ cd api
$ printf 'docs password 1\n' | STRIPE_SECRET_KEY=sk_test_docs STRIPE_WEBHOOK_SECRET=whsec_docs go run ./cmd/api user create --email ada@example.com --role staff --password-stdin
...
$ STRIPE_SECRET_KEY=sk_test_docs STRIPE_WEBHOOK_SECRET=whsec_docs go run ./cmd/api billing status ada@example.com
...
customer	none
entitlement	free	status=-	features=basic
```

## The plan of a user

The effective plan is the highest-ranked plan of a subscription with the status `active`, `trialing`, or `past_due`.
With no such subscription, the plan is `default_plan`. The statuses `incomplete`, `incomplete_expired`, `unpaid`, `canceled`, and `paused` give no plan.
A subscription gets the plan of its first price whose lookup key a plan declares.

When the plan of a user changes, the sync does these things in one transaction:

- It writes the plan and its features to the table `billing_entitlements`.
- With the apikeys module, it sets the apikeys plan of the user to `apikeys_plan`, or to the default apikeys plan when the plan has none.
- With a `role` on the old or the new plan, it changes the role of the user. The new role is the role of the plan, or the default role.
- With the [audit module](../audit.md), it records the event `billing.plan_changed` with the old and the new plan.

The sync changes a role only when the current role is the default role or a role that a billing plan gives. Thus a role that an administrator set stays.
The admin role never changes. An administrator who subscribes gets the plan and its features, and keeps the admin role.

## Gate a feature

In Go, wrap a handler with `Require`. A user without the feature gets 402 with the problem type `plan_required` and the missing feature.
A request without a user gets 401. `Require` reads the plan one time for each request.

```go
app.WithRoutes(func(mux *http.ServeMux) {
	mux.Handle("GET /api/reports/export", billingSvc.Require("export")(exportHandler))
}),
```

```json
{"type":"plan_required","title":"Plan required","status":402,"detail":"Your plan does not include the feature \"export\". Try: upgrade the plan on /pricing.","feature":"export"}
```

Inside a handler, `billingSvc.Has(ctx, "export")` returns the same answer as a boolean.
In Svelte, the root layout loads the billing summary for a signed-in user. Use `hasFeature` from the generated file:

```svelte
<script lang="ts">
	import { hasFeature } from '#lib/billing.gen.js';
	let { data } = $props();
</script>

{#if hasFeature(data.billing, 'export')}
	<a href="/reports/export">Export</a>
{:else}
	<a href="/pricing">Upgrade to export</a>
{/if}
```

The `Feature` type of `billing.gen.ts` lists the declared features, so a wrong name fails `pnpm check`.
A gate in the web app only hides a link. Always gate the API route too.

## Bill for API calls

A plan with `meter` reports the API key calls of each user to that Stripe meter. The module needs the apikeys module for this.
The worker runs one usage pass every 15 minutes. One pass does this for each user on a metered plan who has a Stripe customer:

1. It adds up the key calls of the user for each UTC day of the last 30 days.
2. It compares each day with the number that it sent before, and skips a day with no increase.
3. It sends one meter event with the increase. The event identifier is `apikeys:<user id>:<day>:<total>`, so Stripe drops a repeated event.

A failed send stops the pass for that user. The job then fails, and River tries it again.
Calls that the API has not written to the database yet go in the next pass. See the flush interval in [API keys](api-keys.md#limits-to-know).
To run one pass now, use the command:

<!-- doctest: skip -->
```console
$ go run ./cmd/api billing report-usage
sent 3 meter events
```

Stripe marks Billing Meters "Not Recommended" for new integrations, and points to Metronome. Meters still work. The module sends usage through a `Reporter` interface, so a later reporter can replace the meter.

## The routes

| Route | Who calls it | Answer |
| --- | --- | --- |
| `GET /api/billing/plans` | Anyone | The plans with their Stripe prices. The API keeps the prices for 5 minutes. |
| `GET /api/me/billing` | A signed-in user | The plan, the features, the status, the period end, and whether the user has a customer. |
| `POST /api/me/billing/checkout` | A signed-in user | `{"url": ...}` of a Checkout session for a declared lookup key. |
| `POST /api/me/billing/portal` | A signed-in user | `{"url": ...}` of a Customer Portal session. |
| `POST /api/me/billing/sync` | A signed-in user | The same body as `GET /api/me/billing`, after a sync for the Checkout session. |
| `POST /api/billing/webhook` | Stripe | 200 for a good signature, 400 for a bad one. |

The routes under `/api/me/billing` need a cookie session. An API key gets 403.
A user with a live subscription gets 409 from checkout, with the line `Try: open the billing portal to change the plan.`
The portal answers 409 when the user has no Stripe customer.
These routes are not in the OpenAPI document of the app. The web pages call them through `web/src/lib/server/billing.ts`.

## The operator command

Run the command from the `api/` folder.

| Action | What it does |
| --- | --- |
| `billing check` | Checks the key, the lookup keys, the meters, and the webhook secret. |
| `billing status EMAIL` | Prints the customer, the subscriptions, and the plan of a user. |
| `billing sync EMAIL` | Reads the subscriptions of the user from Stripe now. |
| `billing report-usage` | Runs one usage pass now, and prints the number of meter events sent. |

## The audit trail

With the [audit module](../audit.md), changes to the tables `billing_customers`, `billing_subscriptions`, and `billing_entitlements` write events.
The tables `billing_events` and `billing_usage_reports` have no audit trigger.

If you add the audit module after the billing module, the billing tables have no triggers.
The doctor check `billing-wired` warns about this case and prints the statements. Run `spinup db new audit_billing`, and paste the statements into the new file:

```sql
-- +goose Up
CREATE TRIGGER billing_customers_audit AFTER INSERT OR UPDATE OR DELETE ON public.billing_customers
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'user_id');
CREATE TRIGGER billing_customers_audit_truncate AFTER TRUNCATE ON public.billing_customers
    FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
CREATE TRIGGER billing_subscriptions_audit AFTER INSERT OR UPDATE OR DELETE ON public.billing_subscriptions
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id');
CREATE TRIGGER billing_subscriptions_audit_truncate AFTER TRUNCATE ON public.billing_subscriptions
    FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
CREATE TRIGGER billing_entitlements_audit AFTER INSERT OR UPDATE OR DELETE ON public.billing_entitlements
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'user_id');
CREATE TRIGGER billing_entitlements_audit_truncate AFTER TRUNCATE ON public.billing_entitlements
    FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();

-- +goose Down
DROP TRIGGER billing_entitlements_audit_truncate ON public.billing_entitlements;
DROP TRIGGER billing_entitlements_audit ON public.billing_entitlements;
DROP TRIGGER billing_subscriptions_audit_truncate ON public.billing_subscriptions;
DROP TRIGGER billing_subscriptions_audit ON public.billing_subscriptions;
DROP TRIGGER billing_customers_audit_truncate ON public.billing_customers;
DROP TRIGGER billing_customers_audit ON public.billing_customers;
```

Then run `spinup db migrate up`.

## Limits to know

- The billing entity is the user. spinup has no organizations, so a team cannot share one subscription.
- When you delete a user, the database deletes the billing rows of the user. The Stripe customer stays in Stripe. Delete it in the Dashboard if you must.
- Checkout is the hosted Stripe page. The module has no embedded Checkout and no Stripe Elements.
- The module sells subscriptions only. It has no one-time payments.
- The meter counts API key calls only. The app cannot send its own usage events.
- The module does not make products and prices for you. Make them in the Dashboard.
- The module has no admin page for billing. Use `billing status` and the Stripe Dashboard.
- The module does not use the Stripe Entitlements API or thin events.

## See also

- [generate billing](../commands/generate-billing.md): the generator
- [API keys and plans](api-keys.md): the plans that a billing plan can set
- [Roles](roles.md): the role that a billing plan can give
- [Background jobs](jobs.md): the worker that runs the sync
- [ADR 0032](https://github.com/stopyellingatme/spinup/blob/main/docs/adr/0032-billing.md): the decisions
