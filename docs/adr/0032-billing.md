# ADR 0032: Billing module with Stripe

Status: accepted (owner, 2026-11-07)
Date: 2026-11-07
Extends: ADR 0013 section 1 (modules), ADR 0018 section 5 (fake rules), ADR 0024 (roles), ADR 0025 (audit trail), ADR 0028 (kit migrations), ADR 0030 (API keys)

## Context

A generated app has users, roles, and API key plans with quotas (ADR 0030).
ADR 0030 section 17 says: "A billing hook calls `SetPlan` later."
An app owner who wants paid plans must write the Stripe integration by hand today.

Research on 2026-11-07 found these facts:

- The current Stripe API version is `2026-10-28.endive`. A request sends `Stripe-Version`. A webhook endpoint sends events at its own version.
- The Checkout `ui_mode` values are `hosted_page`, `embedded_page`, `elements`, and `form`. The old names fail. Endive removed `payment_method_types`.
- Since `2025-03-31.basil`, `current_period_end` is on each subscription item, not on the subscription.
- Stripe does not guarantee the order of events. Stripe tells you to dedupe on the event ID, answer 2xx fast, process on a queue, and fetch the object again before you act.
- Hosted Checkout waits up to 10 seconds for the webhook before it redirects. Stripe tells you to also fulfil from the success page with `{CHECKOUT_SESSION_ID}`.
- `Stripe-Signature` is `t=<unix>,v1=<hex>[,v1=<hex>]`. The signature is HMAC-SHA256 of `<t>.<payload>`. The default tolerance is 300 seconds.
- Billing Meters (`POST /v1/billing/meter_events`) take `event_name`, `payload[stripe_customer_id]`, `payload[value]`, `identifier` (dedupe), and `timestamp` (within the last 35 days). Stripe now marks Meters "Not Recommended" for new integrations and points to Metronome. Meters still work.
- stripe-go v87 has a new major version about every six months, and it adds `shopspring/decimal`.
- spinup has no organizations. A user owns each resource.

The owner decided these points on 2026-11-07. They are binding:

1. The feature is an opt-in module `billing`. It sells subscriptions and meters usage.
2. Checkout is Stripe-hosted. Changes go through the hosted Customer Portal. The app never sees card data.
3. A plan gives feature gates. A plan can also select the apikeys plan and grant a role.
4. Usage goes to Stripe Billing Meters behind a `Reporter` interface. Metronome is a later ADR.
5. The metered usage is API key calls.
6. The kit has its own thin Stripe client. It does not use stripe-go.

## Decisions

### 1. Promise and limits

1.1. The billing entity is the user. Each user has zero or one Stripe customer.
1.2. The webhook endpoint is `POST /api/billing/webhook`. It is under `/api/`, so Caddy sends it to the Go API in every frontend mode.
1.3. A webhook only records the event and queues a sync. The sync fetches the state from Stripe. The payload is never the source of truth.
1.4. Out of scope: organizations, embedded Checkout and Elements, one-time payments, Metronome, app-defined usage events, thin events, the Stripe Entitlements API, an `/admin/billing` page, and a command that creates products and prices.

### 2. Dependencies

2.1. No new Go module. The kit client uses `net/http`, `crypto/hmac`, and `encoding/json` (v1, the persisted-JSON rule of ADR 0027).
2.2. The `billing` pack requires `auth` and `jobs`. The `apikeys` link and usage metering work only when the app also has `apikeys`.

### 3. `spinup.toml`

```toml
[billing]
default_plan = "free"            # plan of a user with no live subscription (default "free")
trial_days = 0                   # 0 to 730; 0 means no trial
automatic_tax = false
allow_promotion_codes = false

[[billing.plans]]
name = "free"
features = ["basic"]
apikeys_plan = "free"            # optional

[[billing.plans]]
name = "pro"
prices = ["pro_monthly", "pro_yearly"]   # Stripe price lookup keys
features = ["basic", "export"]
apikeys_plan = "pro"             # optional, needs the apikeys module
role = "pro"                     # optional
meter = "api_calls"              # optional Stripe meter event_name, needs the apikeys module
```

3.1. The plans are an array of tables, so their order is the rank. The first plan is the lowest.
3.2. Rules (checked by `project.BillingDecl.Validate`):
- 1 to 16 plans. A plan name matches `^[a-z][a-z0-9_]{0,31}$` and is unique.
- `default_plan` names a plan. That plan has no `prices`, no `role`, and no `meter`.
- Every other plan has 1 to 8 prices. A price lookup key matches `^[A-Za-z0-9_.-]{1,200}$` and is unique across all plans.
- A feature matches `^[a-z][a-z0-9_]{0,31}$`. A plan has at most 32 features, with no repeats.
- `apikeys_plan` names a plan of `[apikeys]` (after its defaults), and the app has the `apikeys` module.
- `role` names an `[auth]` role. It is not the admin role and not the default role.
- `meter` matches `^[a-z][a-z0-9_]{0,99}$`, and the app has the `apikeys` module.
- `trial_days` is 0 to 730.
3.3. With no `[billing]` table, the defaults are one plan `free` with no features.
3.4. Unknown keys fail, the same as `[apikeys]`.

### 4. Kit API: package `kit/x/billing/stripe`

```go
package stripe // import "github.com/stopyellingatme/spinup/kit/x/billing/stripe"

const APIVersion = "2026-10-28.endive"
const DefaultBaseURL = "https://api.stripe.com"

type Option func(*options)
func WithBaseURL(u string) Option            // tests and STRIPE_API_BASE
func WithHTTPClient(c *http.Client) Option   // default: 20 s timeout
func WithRetries(n int) Option               // default 2 retries (3 tries)

func New(secretKey string, opts ...Option) (*Client, error) // rejects an empty or blank key

type Error struct { Status int; Type, Code, Param, Message, RequestID string }
func (e *Error) Error() string // says what failed and ends with a Try: line; never prints the key

type Customer struct { ID, Email string; Metadata map[string]string }
type CustomerParams struct { Email, Name string; Metadata map[string]string }
type CheckoutParams struct {
	Customer, Price, ClientReferenceID, SuccessURL, CancelURL string
	TrialDays int; AutomaticTax, AllowPromotionCodes bool; Metadata map[string]string
}
type CheckoutSession struct { ID, URL, Customer, ClientReferenceID, Status, PaymentStatus, Subscription string }
type PortalSession struct { ID, URL string }
type Price struct {
	ID, LookupKey, Currency, Nickname string; UnitAmount int64; Active bool
	Recurring *Recurring; Product Product
}
type Recurring struct { Interval string; IntervalCount int; UsageType, Meter string }
type Product struct { ID, Name, Description string }
type Subscription struct {
	ID, Customer, Status string; CancelAtPeriodEnd bool; TrialEnd int64
	Items []SubscriptionItem
}
type SubscriptionItem struct { ID string; Price Price; CurrentPeriodEnd int64 }
type Meter struct { ID, EventName, DisplayName, Status string }
type MeterEvent struct { EventName, Customer, Identifier string; Value int64; Timestamp time.Time }

func (c *Client) CreateCustomer(ctx context.Context, p CustomerParams, idempotencyKey string) (*Customer, error)
func (c *Client) CreateCheckoutSession(ctx context.Context, p CheckoutParams, idempotencyKey string) (*CheckoutSession, error)
func (c *Client) GetCheckoutSession(ctx context.Context, id string) (*CheckoutSession, error)
func (c *Client) CreatePortalSession(ctx context.Context, customer, returnURL string) (*PortalSession, error)
func (c *Client) ListSubscriptions(ctx context.Context, customer string) ([]Subscription, error)
func (c *Client) ListPrices(ctx context.Context, lookupKeys []string) ([]Price, error)
func (c *Client) ListMeters(ctx context.Context) ([]Meter, error)
func (c *Client) CreateMeterEvent(ctx context.Context, e MeterEvent) error
func (c *Client) CheckKey(ctx context.Context) error // GET /v1/balance; a cheap key check for `api billing check`

type Event struct { ID, Type, APIVersion string; Created int64; Livemode bool; Data struct{ Object json.RawMessage } }
func (e Event) Customer() string // data.object.customer, or data.object.id when the object is a customer

const DefaultTolerance = 300 * time.Second
func VerifyWebhook(payload []byte, header string, secrets []string, now time.Time) (Event, error)
func SignatureHeader(payload []byte, secret string, t time.Time) string // for tests and the fake
```

4.1. Requests are `application/x-www-form-urlencoded` with Stripe's bracket syntax (`metadata[user_id]=7`, `lookup_keys[]=a`). The client sorts the form keys, so a request body is deterministic.
4.2. Each request sends `Authorization: Bearer <key>`, `Stripe-Version: APIVersion`, and `User-Agent: spinup-kit-billing`. Each POST sends `Idempotency-Key`. When the caller gives no key, the client makes a random one and keeps it for the retries of that call.
4.3. The client retries on a network error, 409 with `lock_timeout`, 429, and 5xx, and when `Stripe-Should-Retry: true`. It does not retry when `Stripe-Should-Retry: false`. The backoff is 500 ms, then 1 s, with jitter, and it obeys the context.
4.4. List calls follow `has_more` with `starting_after`, with `limit=100` and at most 10 pages.
4.5. `CreateCheckoutSession` sends `mode=subscription`, `ui_mode=hosted_page`, `line_items[0][price]`, and `line_items[0][quantity]=1` for a licensed price. A metered price has no quantity. It also sends `subscription_data[trial_period_days]` when TrialDays > 0, `automatic_tax[enabled]=true`, and `allow_promotion_codes=true` when set. `ListPrices` sends `active=true` and `expand[]=data.product`.
4.6. `VerifyWebhook` accepts the event when any `v1` value matches any secret (rotation). It compares with `hmac.Equal`. It rejects a missing header, a missing or non-numeric `t`, no `v1`, a time outside the tolerance in either direction, an empty secret list, a blank secret, and a payload that is not a JSON event with `id` and `type`.

### 5. The fake: package `kit/x/billing/stripe/stripefake`

```go
type Options struct {
	Secret        string        // webhook signing secret; required
	WebhookURL    string        // where events go; empty means record only
	Prices        []stripe.Price // the catalog, with lookup keys and products
	Meters        []stripe.Meter
	Now           func() time.Time
	Deliver       *http.Client  // sends the webhooks; default http.DefaultClient
}
func New(o Options) *Fake           // does not listen
func (f *Fake) Handler() http.Handler
func (f *Fake) Transport() http.RoundTripper // in process, for tests without sockets
func (f *Fake) Start() (url string, err error) // listens on 127.0.0.1 (httptest server)
func (f *Fake) URL() string         // empty before Start
func (f *Fake) Close()
func (f *Fake) CompleteCheckout(sessionID string) (subscriptionID string, err error)
func (f *Fake) SetStatus(subscriptionID, status string) error // active, past_due, unpaid, canceled, paused, trialing
func (f *Fake) Resend(eventID string) error
func (f *Fake) Events() []stripe.Event
func (f *Fake) MeterEvents() []stripe.MeterEvent
func (f *Fake) Requests() []Request // method, path, form, headers (for assertions)
func (f *Fake) FailNext(path string, status int, shouldRetry string) // error injection
```

5.1. The fake follows ADR 0018 section 5. Each rule below has a test in `fidelity_test.go`:
- 401 without a Bearer key that starts with `sk_test_` or `rk_test_`.
- 400 `parameter_unknown` for an unknown form key on each endpoint it serves.
- 400 when `Stripe-Version` is missing or is not `stripe.APIVersion`.
- A repeated POST with the same `Idempotency-Key` and the same body returns the first response. A different body answers 400 `idempotency_key_in_use`.
- 400 `resource_missing` for an unknown customer, price, session, or subscription.
- Checkout: `mode=subscription`, `ui_mode=hosted_page`, an active recurring price, quantity rules of section 4.5, and `customer` must exist.
- A meter event with the same `identifier` within 24 hours is ignored. A timestamp older than 35 days or more than 5 minutes ahead answers 400.
- `ListSubscriptions` with `status=all` includes canceled subscriptions.
5.2. `CompleteCheckout` creates an `active` subscription (or `trialing` with a trial) with one item whose `current_period_end` is 30 days ahead. It sends `checkout.session.completed` and `customer.subscription.created`. `SetStatus` sends `customer.subscription.updated`, or `customer.subscription.deleted` for `canceled`. Each event is signed with `SignatureHeader`.

### 6. Kit API: package `kit/x/billing`

```go
package billing // import "github.com/stopyellingatme/spinup/kit/x/billing"

const SessionPrefix = "/api/me/billing"
const WebhookPath = "/api/billing/webhook"

type Plan struct { Name string; Prices, Features []string; APIKeysPlan, Role, Meter string }
type Config struct {
	DefaultPlan string; TrialDays int; AutomaticTax, AllowPromotionCodes bool
	Plans []Plan // rank order, lowest first
}
func (c Config) Validate() error           // the rules of 3.2 that need no other table
func (c Config) Plan(name string) (Plan, bool)

type Option func(*options)
func WithLogger(l *slog.Logger) Option
func WithClock(now func() time.Time) Option
func WithJobs(c *jobs.Client) Option          // required for the webhook route
func WithAPIKeys(s *apikeys.Store) Option     // plan link and usage source
func WithRoles(r auth.Roles) Option           // required when a plan has a role
func WithPublicOrigin(origin string) Option   // required for checkout and portal URLs
func WithWebhookSecrets(secrets ...string) Option // required for the webhook route
func WithReporter(r Reporter) Option          // default: MeterReporter on the client

func New(pool *pgxpool.Pool, cfg Config, client *stripe.Client, opts ...Option) (*Service, error)

func (s *Service) Routes(a *auth.Auth) func(*http.ServeMux)
func (s *Service) Register(w *jobs.Worker)    // sync job, and the usage job when a plan has a meter
func (s *Service) Command() func(context.Context, []string) error
func (s *Service) Require(features ...string) httpx.Middleware
func (s *Service) Has(ctx context.Context, feature string) (bool, error)
func (s *Service) Entitlement(ctx context.Context, userID int64) (Entitlement, error)
func (s *Service) Sync(ctx context.Context, customerID string) error
func (s *Service) ReportUsage(ctx context.Context) error // one pass of the usage job

type Entitlement struct {
	Plan string; Features []string; Status string // "" when no subscription
	CurrentPeriodEnd time.Time; CancelAtPeriodEnd bool; HasCustomer bool
}

type Reporter interface { Report(ctx context.Context, e stripe.MeterEvent) error }
type MeterReporter struct{ Client *stripe.Client }

type SyncArgs struct { CustomerID string `json:"customer_id"`; EventID string `json:"event_id"` } // Kind "billing.sync_customer"
type UsageArgs struct{}                                                                     // Kind "billing.report_usage"
```

6.1. `New` validates `cfg` and the options. It does not reach Stripe or the database.
6.2. `Has` and `Require` read the user from `auth.UserFrom`. A missing entitlement row means the default plan. `Require` caches the entitlement in the request context, so a request reads it once.

### 7. Tables (kit migration `api/db/kit/00010_billing.sql`)

```sql
CREATE TABLE billing_customers (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    customer_id text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE billing_subscriptions (
    id text PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    customer_id text NOT NULL,
    status text NOT NULL,
    price_lookup_key text NOT NULL DEFAULT '',
    plan text NOT NULL DEFAULT '',
    current_period_end timestamptz,
    cancel_at_period_end boolean NOT NULL DEFAULT false,
    trial_end timestamptz,
    synced_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX billing_subscriptions_user_id_idx ON billing_subscriptions (user_id);
CREATE TABLE billing_entitlements (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    plan text NOT NULL,
    features text[] NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE billing_events (
    event_id text PRIMARY KEY,
    type text NOT NULL,
    customer_id text NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    error text NOT NULL DEFAULT ''
);
CREATE TABLE billing_usage_reports (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day date NOT NULL,
    reported bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, day)
);
```

7.1. The audit triggers attach to `billing_customers`, `billing_subscriptions`, and `billing_entitlements` when the audit functions exist, with the same `DO` block pattern as `00009_api_keys.sql`. `billing_events` and `billing_usage_reports` are not audited.
7.2. The number `00010` belongs to spinup (ADR 0028 section 1.5).
7.3. Deleting a user deletes the local rows. The Stripe customer stays in Stripe. The docs say so.

### 8. The webhook

8.1. `POST /api/billing/webhook` has no auth. The body limit is 256 KiB (413 above it).
8.2. A bad or missing signature answers 400 and logs the reason at warn level. The response body does not say which check failed.
8.3. The handled types are `checkout.session.completed`, `checkout.session.async_payment_succeeded`, `customer.subscription.created`, `.updated`, `.deleted`, `.paused`, `.resumed`, `invoice.paid`, and `invoice.payment_failed`. Another type answers 200 with no row and no job.
8.4. For a handled type, one transaction runs `INSERT INTO billing_events ... ON CONFLICT (event_id) DO NOTHING`. When the insert adds a row, it also runs `jobs.InsertTx(SyncArgs{customer, event_id}, Queue(jobs.DefaultQueue), MaxAttempts(20))`. Then the route answers 200. A repeated event answers 200 with no new job.
8.5. A database error answers 500, so Stripe retries.
8.6. The route sets `Cache-Control: no-store`. It is not under `/api/me/`, so the apikeys middleware does not apply to it, and a Bearer header has no effect.

### 9. Sync and plan rules

9.1. `Sync(customer)` runs in one transaction with `pg_advisory_xact_lock(hashtext('billing:' || customer))`.
9.2. It finds the user through `billing_customers`. An unknown customer is a warning, the event row gets `error = 'unknown customer'`, and the job succeeds.
9.3. It calls `ListSubscriptions(customer)` after it takes the lock, so the last sync always writes the newest state. It upserts each subscription. It sets `plan` from the first item price whose lookup key a plan declares. It deletes the local rows of this customer that Stripe did not return.
9.4. The effective plan is the highest-ranked plan of a subscription with status `active`, `trialing`, or `past_due`. With no such subscription, the effective plan is `default_plan`. The other statuses (`incomplete`, `incomplete_expired`, `unpaid`, `canceled`, `paused`) give no plan.
9.5. When the effective plan or its features change, the sync:
- upserts `billing_entitlements`
- with `WithAPIKeys`, sets the apikeys plan to the plan's `apikeys_plan`, or to the apikeys default plan when the plan has none. It uses the new `apikeys.Store.SetPlanForUser(ctx, tx, userID, plan)` in the same transaction.
- with a role on the old or the new plan, changes `users.role` only when the current role is the default role or a role that a billing plan grants. The new role is the plan's role, or the default role. The admin role never changes.
- records `audit.Record` with action `billing.plan_changed`, table `billing_entitlements`, record ID the user ID, actor kind `KindSystem`, and the old and new plan. This happens only when the audit tables exist.
9.6. Then it sets `processed_at` on the event row of the job. A Stripe error fails the job, and River retries it.

### 10. Routes

All `/api/me/billing` routes need a cookie session (`a.RequireUser`, and the apikeys session-only rule of section 11). They answer `Cache-Control: no-store`.

| Route | Body | Answer |
|---|---|---|
| `GET /api/me/billing` | | `{plan, features, status, current_period_end, cancel_at_period_end, has_customer}` |
| `POST /api/me/billing/checkout` | `{"price": "<lookup key>"}` | `{"url": "..."}` |
| `POST /api/me/billing/portal` | `{}` | `{"url": "..."}` |
| `POST /api/me/billing/sync` | `{"session_id": "cs_..."}` | the same body as `GET /api/me/billing` |
| `GET /api/billing/plans` | | `{plans: [{name, features, prices: [{lookup_key, currency, unit_amount, interval, interval_count, usage_type, product_name}]}]}` |

10.1. Checkout: the price must be a declared lookup key (400 otherwise). A user with a subscription in `active`, `trialing`, or `past_due` gets 409 with "Try: open the billing portal to change the plan." The route creates the customer when it is missing (idempotency key `customer:<user id>`, metadata `user_id`). The session has `client_reference_id = <user id>`, metadata `user_id`, `success_url = <origin>/settings/billing?session_id={CHECKOUT_SESSION_ID}`, and `cancel_url = <origin>/pricing`.
10.2. Portal: 409 when the user has no customer. The return URL is `<origin>/settings/billing`.
10.3. Sync: the session must have `client_reference_id` equal to the user ID and the user's customer. Otherwise the route answers 404. Then it runs `Sync` and answers the entitlement.
10.4. Plans: public, `Cache-Control: public, max-age=300`. The service caches the Stripe prices for 5 minutes. When Stripe fails and there is no cache, the route answers 503. A declared lookup key that Stripe does not return is left out and logged once.
10.5. Bodies use `httpx.DecodeJSON` (strict, ADR 0027). Errors are problem JSON with a `Try:` line.

### 11. The apikeys changes

11.1. `apikeys.WithSessionOnly(prefixes ...string) StoreOption` adds path prefixes that an API key cannot call. The middleware answers 403 on them, the same as `/api/me/keys`. The billing wiring passes `billing.SessionPrefix`.
11.2. `(*apikeys.Store).SetPlanForUser(ctx context.Context, tx pgx.Tx, userID int64, plan string) error` sets the plan in the caller's transaction. It returns `ErrUnknownPlan` for an undeclared plan and `ErrUserNotFound` when no row changes. `SetPlan` keeps its behavior.
11.3. `(*apikeys.Store).DefaultPlan() string` returns the configured default plan.

### 12. Gates

12.1. `Require(features...)` answers 401 without a user. It answers 402 with problem type `plan_required`, the missing feature, and "Try: upgrade the plan on /pricing" when the plan lacks any listed feature. A database error answers 500.
12.2. The web pack gets `hasFeature(billing, feature)` in `web/src/lib/billing.gen.ts`. The root layout loads `GET /api/me/billing` for a signed-in user only when the module is on.

### 13. Usage metering

13.1. `Register` adds the periodic `UsageArgs` job every 15 minutes when a plan has a meter and `WithAPIKeys` is set.
13.2. One pass (`ReportUsage`) does this for each user whose entitlement plan has a meter and who has a customer:
- Sum `api_key_usage.count` over the user's keys, for each UTC day of the last 30 days.
- Compare each day with `billing_usage_reports.reported`. Skip the days with no increase.
- Send one meter event: `event_name = meter`, `customer`, `value = total - reported`, `identifier = apikeys:<user id>:<day>:<total>`, `timestamp` = the end of that day, or now for today.
- Then upsert `reported = total`.
13.3. A failed send stops the pass for that user and fails the job, so River retries. The identifier makes a retry safe (Stripe dedupes it).
13.4. Usage that the apikeys store has not flushed yet goes in the next pass.

### 14. The app command `billing`

`api billing check` checks the key (`CheckKey`), each declared lookup key (active and recurring), each declared meter (active), and that a webhook secret is set. It prints one line for each check and exits non-zero on a failure.
`api billing status <email>` prints the customer, the subscriptions, and the entitlement.
`api billing sync <email>` runs `Sync` for the user's customer.

### 15. Secrets, module, and wiring

15.1. The secrets are `STRIPE_SECRET_KEY` and `STRIPE_WEBHOOK_SECRET`, read with `kit/config` (`_FILE` works). The webhook secret can be a comma list for rotation. `STRIPE_API_BASE` overrides the base URL (tests only).
15.2. `deploy/compose.yml.tmpl` (managed) adds `STRIPE_SECRET_KEY_FILE` and `STRIPE_WEBHOOK_SECRET_FILE` (`/run/secrets/...`) to the api and worker services under `has "billing"`.
15.3. `main.go.tmpl` (api pack, scaffold), under `has "billing"`: load the env, build the client and the service with `WithJobs`, `WithRoles(roles.Set)`, `WithPublicOrigin`, `WithWebhookSecrets`, and `WithAPIKeys(keyStore)` under `has "apikeys"`. Add `apikeys.WithSessionOnly(billing.SessionPrefix)` to `NewStore`, `app.WithRoutes(billingSvc.Routes(a))`, and `app.WithCommand("billing", billingSvc.Command())`.
15.4. `cmd/worker/main.go.tmpl` (jobs pack), under `has "billing"`: build the service the same way and call `Register(w)`.
15.5. The pack `billing` has the files of section 16, `requires = ["auth", "jobs"]`, and next steps in `internal/modules/billing.go`.
15.6. The doctor check `billing-wired` parses `api/cmd/api/main.go` and `api/cmd/worker/main.go`. It checks `billing.New`, `.Routes`, `.Register`, `WithSessionOnly` when the app has apikeys, the two compose secrets, and the audit triggers when audit is on.
15.7. The doctor check `billing-drift` compares the generated files with a fresh render of `[billing]`.

### 16. Generated files

| Path | Class | Content |
|---|---|---|
| `api/db/kit/00010_billing.sql` | seed | section 7 |
| `api/internal/billing/billing.gen.go` | managed | `var Config = kitbilling.Config{...}`, plan and feature constants |
| `web/src/lib/billing.gen.ts` | managed | plan and feature names as types, `hasFeature` |
| `web/src/lib/server/billing.ts` | managed | the API calls for the pages |
| `web/src/routes/pricing/+page.svelte`, `+page.server.ts` | scaffold | public plans, Subscribe buttons |
| `web/src/routes/settings/billing/+page.svelte`, `+page.server.ts` | scaffold | the plan, the status, Upgrade, Manage billing, the success-page sync |

`spinup g billing` writes the two `.gen` files. The pack ships them rendered from the defaults of 3.3. The next steps of `spinup module add billing` tell the owner to add `[billing]` and run `spinup g billing`.

### 17. Errors

Every user-facing error says what failed and why, and ends with a `Try:` line. A Stripe error includes the Stripe request ID and never the key.

### 18. Upgrade of existing apps

`main.go` and the worker are scaffold files, so `spinup module add billing` prints the wiring as next steps (`internal/upgrade/module.go`). The compose change is in a managed file, so the 3-way merge applies it.

### 19. Tests

19.1. `kit/x/billing/stripe`: the form encoding, the headers, the retries and `Stripe-Should-Retry`, the idempotency key reuse, pagination, the error type, and each rule of 4.6. `stripefake`: each rule of 5.1.
19.2. `kit/x/billing` (Postgres): the webhook (signature, dedupe, unknown type, body limit, 500 on a database error), the sync rules of section 9 (ranks, statuses, role rules, admin kept, apikeys plan, audit, ordering of two events), each route of section 10, the gates, and the usage pass (deltas, identifiers, retry).
19.3. `kit/x/apikeys`: `WithSessionOnly`, `SetPlanForUser`, `DefaultPlan`.
19.4. `internal/project`: each rule of 3.2.
19.5. Golden tree `billing`. Script tests `billing_module`, `billing_module_add`, `billing_bad_declaration`, `billing_drift`, `billing_wired`.
19.6. `app_gate_billing`: the flow of the plan's verification section, with the fake started by the script command `stripefake`.

### 20. Docs

`docs/build/billing.md`: enable the module, the Stripe Dashboard steps (products, prices with lookup keys, a meter, the webhook endpoint at `2026-10-28.endive`, the portal settings), the secrets, local work with `stripe listen --forward-to localhost:5173/api/billing/webhook`, gates in Go and Svelte, the role and apikeys rules, usage metering, and the limits. Add the module to the module table of the concepts page.

## Consequences

- An app can sell subscriptions and meter API calls with one module and no new dependency.
- spinup owns a small Stripe client. A Stripe API version change is a kit change, tested against the fake and a manual test-mode run.
- Stripe recommends Metronome for new usage billing. A Metronome reporter can replace `MeterReporter` later.
- The billing entity is the user until spinup has organizations.

## Appendix A: Units

### U0 (main loop): ADR and pre-seed
This ADR, `internal/project/billing.go` with its tests and the `project.go` hooks, the stubs, the CLAUDE.md line, and the `wire_gen.go` imports.

### K1: `kit/x/billing/stripe` and `stripefake`
Files: `kit/x/billing/stripe/**`. Check: `T ./kit/x/billing/stripe/...`. Sections 4, 5, 19.1.

### K2: apikeys hooks
Files: `kit/x/apikeys/store.go`, `kit/x/apikeys/middleware.go`, and their tests. Check: `T ./kit/x/apikeys/...`. Section 11, 19.3.

### K3: `kit/x/billing` (after K1, K2)
Files: `kit/x/billing/*.go`, `internal/templates/packs/billing/files/api/db/kit/00010_billing.sql`. Check: `T ./kit/x/billing/...` with `DATABASE_URL`. Sections 6 to 10, 12 to 14, 19.2.

### P1: pack and wiring (after K3)
Files: `internal/templates/packs/billing/**` except the migration, the `has "billing"` blocks in the api pack `main.go.tmpl`, the jobs pack worker, `deploy/compose.yml.tmpl`, the web pack layout, user menu, and `openapi.yaml.tmpl`, `internal/modules/billing.go`, `internal/upgrade/module.go`, `internal/templates/golden_billing_test.go`, `testdata/golden/billing/**`, the golden trees that change, `internal/templates/packs.digest`. Check: `G billing`, `G apikeys`, `G deploy`, `G api`, `T ./internal/modules/... ./internal/upgrade/... ./internal/templates/...`. Sections 15.2 to 15.5, 16.

### G1: generator and drift (after P1)
Files: `internal/gen/billing/**`, `internal/doctor/billingdrift/**`, `testdata/script/billing_drift.txtar`. Check: `T ./internal/gen/billing/... ./internal/doctor/billingdrift/...`, `S billing_drift`. Sections 15.7, 16.

### D1: wired check, docs, scripts, gate (after P1, G1)
Files: `internal/doctor/billingwired/**`, `docs/build/billing.md`, the concepts module table, `testdata/script/billing_module.txtar`, `billing_module_add.txtar`, `billing_bad_declaration.txtar`, `billing_wired.txtar`, `app_gate_billing.txtar`, the `stripefake` script command in `internal/testutil/scripttest`. Check: `T ./internal/doctor/billingwired/...`, `make doctest`, `S billing_*`. Sections 15.6, 19.5, 19.6, 20.

### R1: review (after D1)
Read-only review of the integrated diff: the signature check, the session-only routes, the role rules, idempotency, and secret handling.

### Main loop after the units
Merge, the final gate (`make build lint test golden script doctest`, `app_gate_billing`, `app_gate_apikeys`), and a manual Stripe test-mode check against the todo app.
