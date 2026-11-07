package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/jobs"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

// SessionPrefix starts the paths of the routes that need a cookie session.
// The wiring passes it to apikeys.WithSessionOnly.
const SessionPrefix = "/api/me/billing"

// WebhookPath is the path of the Stripe webhook endpoint.
const WebhookPath = "/api/billing/webhook"

// Plan is one plan of [billing] in spinup.toml.
type Plan struct {
	// Name is the plan name.
	Name string
	// Prices are the Stripe price lookup keys that buy the plan.
	Prices []string
	// Features are the feature names that the plan gives.
	Features []string
	// APIKeysPlan is the apikeys plan of a user with this plan, or "" for the apikeys default plan.
	APIKeysPlan string
	// Role is the role that the plan grants, or "".
	Role string
	// Meter is the Stripe meter event name for the API key calls, or "".
	Meter string
}

// Config is the [billing] table of spinup.toml, as spinup g billing writes it.
type Config struct {
	// DefaultPlan is the plan of a user with no live subscription.
	DefaultPlan string
	// TrialDays is the trial length of a new subscription, or 0 for no trial.
	TrialDays int
	// AutomaticTax turns on Stripe Tax in Checkout.
	AutomaticTax bool
	// AllowPromotionCodes lets a customer enter a promotion code in Checkout.
	AllowPromotionCodes bool
	// Plans are the plans in rank order, lowest first.
	Plans []Plan
}

var (
	namePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	pricePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,200}$`)
	meterPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,99}$`)
)

const configHint = "\nTry: correct [billing] in spinup.toml and run spinup g billing."

// Validate checks the rules of ADR 0032 section 3.2 that need no other table.
// The roles and the apikeys plans are checked by New and by the sync.
func (c Config) Validate() error {
	var problems []error
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf("billing: "+format+configHint, args...))
	}
	if len(c.Plans) < 1 || len(c.Plans) > 16 {
		fail("the plan count %d must be 1 to 16", len(c.Plans))
	}
	if c.TrialDays < 0 || c.TrialDays > 730 {
		fail("TrialDays %d must be 0 to 730", c.TrialDays)
	}
	names := map[string]bool{}
	prices := map[string]string{}
	foundDefault := false
	for i, p := range c.Plans {
		label := fmt.Sprintf("plan %q", p.Name)
		if !namePattern.MatchString(p.Name) {
			fail("plan %d: the name %q does not match %s", i+1, p.Name, namePattern.String())
		}
		if names[p.Name] {
			fail("%s is declared more than once", label)
		}
		names[p.Name] = true
		isDefault := p.Name == c.DefaultPlan
		foundDefault = foundDefault || isDefault
		switch {
		case isDefault && len(p.Prices) > 0:
			fail("%s is the default plan, so it must have no prices", label)
		case !isDefault && (len(p.Prices) < 1 || len(p.Prices) > 8):
			fail("%s must have 1 to 8 prices (it has %d)", label, len(p.Prices))
		}
		for _, price := range p.Prices {
			if !pricePattern.MatchString(price) {
				fail("%s: the price lookup key %q does not match %s", label, price, pricePattern.String())
			} else if other, ok := prices[price]; ok {
				fail("%s: the price lookup key %q is also on plan %q", label, price, other)
			}
			prices[price] = p.Name
		}
		if len(p.Features) > 32 {
			fail("%s has %d features, and the limit is 32", label, len(p.Features))
		}
		seen := map[string]bool{}
		for _, f := range p.Features {
			if !namePattern.MatchString(f) {
				fail("%s: the feature %q does not match %s", label, f, namePattern.String())
			}
			if seen[f] {
				fail("%s lists the feature %q more than once", label, f)
			}
			seen[f] = true
		}
		if p.APIKeysPlan != "" && !namePattern.MatchString(p.APIKeysPlan) {
			fail("%s: the apikeys plan %q does not match %s", label, p.APIKeysPlan, namePattern.String())
		}
		if isDefault && p.Role != "" {
			fail("%s is the default plan, so it must have no role", label)
		}
		if p.Role != "" && !namePattern.MatchString(p.Role) {
			fail("%s: the role %q does not match %s", label, p.Role, namePattern.String())
		}
		switch {
		case p.Meter == "":
		case isDefault:
			fail("%s is the default plan, so it must have no meter", label)
		case !meterPattern.MatchString(p.Meter):
			fail("%s: the meter %q does not match %s", label, p.Meter, meterPattern.String())
		}
	}
	if !foundDefault {
		declared := make([]string, 0, len(c.Plans))
		for _, p := range c.Plans {
			declared = append(declared, p.Name)
		}
		fail("DefaultPlan %q is not a declared plan (declared: %s)", c.DefaultPlan, strings.Join(declared, ", "))
	}
	return errors.Join(problems...)
}

// Plan returns the plan with the name, and false when no plan has it.
func (c Config) Plan(name string) (Plan, bool) {
	for _, p := range c.Plans {
		if p.Name == name {
			return p, true
		}
	}
	return Plan{}, false
}

// rank returns the index of the plan, or -1.
func (c Config) rank(name string) int {
	return slices.IndexFunc(c.Plans, func(p Plan) bool { return p.Name == name })
}

// planOfPrice returns the plan that declares the price lookup key.
func (c Config) planOfPrice(lookupKey string) (Plan, bool) {
	for _, p := range c.Plans {
		if slices.Contains(p.Prices, lookupKey) {
			return p, true
		}
	}
	return Plan{}, false
}

// lookupKeys returns every declared price lookup key, in plan and price order.
func (c Config) lookupKeys() []string {
	var keys []string
	for _, p := range c.Plans {
		keys = append(keys, p.Prices...)
	}
	return keys
}

func (c Config) clone() Config {
	plans := make([]Plan, len(c.Plans))
	for i, p := range c.Plans {
		p.Prices = slices.Clone(p.Prices)
		p.Features = slices.Clone(p.Features)
		plans[i] = p
	}
	c.Plans = plans
	return c
}

type options struct {
	logger   *slog.Logger
	now      func() time.Time
	jobs     *jobs.Client
	keys     *apikeys.Store
	roles    *auth.Roles
	origin   string
	secrets  []string
	reporter Reporter
}

// Option configures New.
type Option func(*options)

// WithLogger selects the logger. The default is slog.Default().
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithClock selects the clock of the webhook check, the plans cache, and the usage days.
func WithClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

// WithJobs selects the jobs client that the webhook uses to queue the sync.
// It is required for the webhook route.
func WithJobs(c *jobs.Client) Option { return func(o *options) { o.jobs = c } }

// WithAPIKeys links the apikeys module: the sync sets the apikeys plan, and
// the usage job reads the API key calls.
func WithAPIKeys(s *apikeys.Store) Option { return func(o *options) { o.keys = s } }

// WithRoles selects the declared roles. It is required when a plan has a role.
func WithRoles(r auth.Roles) Option {
	r.Names = slices.Clone(r.Names)
	return func(o *options) { o.roles = &r }
}

// WithPublicOrigin sets the origin of the web app, such as https://app.example.com.
// The checkout and portal return URLs start with it. It is required.
func WithPublicOrigin(origin string) Option { return func(o *options) { o.origin = origin } }

// WithWebhookSecrets sets the webhook signing secrets. Each value can be a
// comma list, for rotation. Blank parts are ignored. It is required for the
// webhook route.
func WithWebhookSecrets(secrets ...string) Option {
	return func(o *options) {
		for _, value := range secrets {
			for _, part := range strings.Split(value, ",") {
				if part = strings.TrimSpace(part); part != "" {
					o.secrets = append(o.secrets, part)
				}
			}
		}
	}
}

// WithReporter selects where the usage job sends meter events. The default is
// a MeterReporter on the Stripe client.
func WithReporter(r Reporter) Option { return func(o *options) { o.reporter = r } }

// Service runs the billing routes, the sync, the gates, and usage metering.
// It is safe for concurrent use.
type Service struct {
	pool     *pgxpool.Pool
	cfg      Config
	client   *stripe.Client
	logger   *slog.Logger
	now      func() time.Time
	jobs     *jobs.Client
	keys     *apikeys.Store
	roles    *auth.Roles
	origin   string
	secrets  []string
	reporter Reporter

	webhookOnce sync.Once

	catalogMu     sync.Mutex
	catalogAt     time.Time
	catalogPrices map[string]stripe.Price

	warnMu sync.Mutex
	warned map[string]bool
}

func newError(format string, args ...any) error {
	return fmt.Errorf("billing: cannot create the service: "+format, args...)
}

// ErrNotConfigured reports a service with no Stripe client (STRIPE_SECRET_KEY is
// not set). The routes that need Stripe answer 503, and a sync fails with it.
var ErrNotConfigured = errors.New("billing: Stripe is not configured, because STRIPE_SECRET_KEY is not set.\nTry: set STRIPE_SECRET_KEY and restart the API and the worker")

// New validates cfg and the options. It does not reach Stripe or the database.
// A nil client is allowed, so the app starts (and migrates) without Stripe
// secrets. Then the routes that need Stripe answer 503, and a sync and a usage
// pass fail with ErrNotConfigured.
func New(pool *pgxpool.Pool, cfg Config, client *stripe.Client, opts ...Option) (*Service, error) {
	if pool == nil {
		return nil, newError("the database pool is nil\nTry: pass the pool from db.Open.")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.clone()
	o := options{logger: slog.Default(), now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	if o.now == nil {
		o.now = time.Now
	}
	origin, err := normalizeOrigin(o.origin)
	if err != nil {
		return nil, err
	}
	for _, p := range cfg.Plans {
		if p.Role == "" {
			continue
		}
		if o.roles == nil {
			return nil, newError("plan %q grants the role %q, and no roles are set\nTry: pass billing.WithRoles(roles.Set).", p.Name, p.Role)
		}
		if err := o.roles.Validate(); err != nil {
			return nil, err
		}
		switch {
		case !o.roles.Valid(p.Role):
			return nil, newError("plan %q: the role %q is not declared (declared: %s)\nTry: correct [billing] or [auth] in spinup.toml and run spinup g billing.", p.Name, p.Role, strings.Join(o.roles.Names, ", "))
		case p.Role == o.roles.Admin:
			return nil, newError("plan %q: the role %q is the admin role, and a plan cannot grant it\nTry: grant another role.", p.Name, p.Role)
		case p.Role == o.roles.Default:
			return nil, newError("plan %q: the role %q is the default role, and a plan cannot grant it\nTry: grant another role.", p.Name, p.Role)
		}
	}
	if o.reporter == nil && client != nil {
		o.reporter = MeterReporter{Client: client}
	}
	if o.reporter == nil {
		o.reporter = notConfigured{}
	}
	return &Service{pool: pool, cfg: cfg, client: client, logger: o.logger, now: o.now, jobs: o.jobs, keys: o.keys, roles: o.roles,
		origin: origin, secrets: o.secrets, reporter: o.reporter, warned: map[string]bool{}}, nil
}

func normalizeOrigin(origin string) (string, error) {
	if strings.TrimSpace(origin) == "" {
		return "", newError("the public origin is not set\nTry: pass billing.WithPublicOrigin with the web app origin, such as https://app.example.com.")
	}
	u, err := url.Parse(strings.TrimRight(origin, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", newError("the public origin %q is not an HTTP origin\nTry: use a scheme and a host only, such as https://app.example.com.", origin)
	}
	return u.Scheme + "://" + u.Host, nil
}

// warnOnce logs the message at warn level once for each key.
func (s *Service) warnOnce(ctx context.Context, key, msg string, args ...any) {
	s.warnMu.Lock()
	first := !s.warned[key]
	s.warned[key] = true
	s.warnMu.Unlock()
	if first {
		s.logger.WarnContext(ctx, msg, args...)
	}
}

// hasMeter reports whether a plan has a meter.
func (s *Service) hasMeter() bool {
	return slices.ContainsFunc(s.cfg.Plans, func(p Plan) bool { return p.Meter != "" })
}

// Entitlement is the effective plan of a user.
type Entitlement struct {
	// Plan is the effective plan name.
	Plan string
	// Features are the features of the plan.
	Features []string
	// Status is the status of the subscription that gives the plan, or "" when there is no subscription.
	Status string
	// CurrentPeriodEnd is the end of the current period of that subscription, or zero.
	CurrentPeriodEnd time.Time
	// CancelAtPeriodEnd is true when that subscription ends at the period end.
	CancelAtPeriodEnd bool
	// HasCustomer is true when the user has a Stripe customer.
	HasCustomer bool
}

// Reporter sends one usage event.
type Reporter interface {
	// Report sends the meter event. The identifier makes a retry safe.
	Report(ctx context.Context, e stripe.MeterEvent) error
}

// MeterReporter sends usage events to Stripe Billing Meters.
type MeterReporter struct {
	// Client is the Stripe client.
	Client *stripe.Client
}

// Report sends the event with CreateMeterEvent.
// notConfigured is the reporter of a service with no Stripe client.
type notConfigured struct{}

func (notConfigured) Report(context.Context, stripe.MeterEvent) error { return ErrNotConfigured }

func (m MeterReporter) Report(ctx context.Context, e stripe.MeterEvent) error {
	if m.Client == nil {
		return errors.New("billing: cannot report usage: the Stripe client is nil\nTry: set MeterReporter.Client")
	}
	return m.Client.CreateMeterEvent(ctx, e)
}

// SyncArgs are the arguments of the sync job.
type SyncArgs struct {
	// CustomerID is the Stripe customer to sync.
	CustomerID string `json:"customer_id"`
	// EventID is the webhook event that queued the job, or "".
	EventID string `json:"event_id"`
}

// Kind returns "billing.sync_customer".
func (SyncArgs) Kind() string { return "billing.sync_customer" }

// UsageArgs are the arguments of the periodic usage job.
type UsageArgs struct{}

// Kind returns "billing.report_usage".
func (UsageArgs) Kind() string { return "billing.report_usage" }

// Register registers the sync job and, when a plan has a meter and WithAPIKeys
// is set, the usage job every 15 minutes.
func (s *Service) Register(w *jobs.Worker) {
	jobs.Register(w, func(ctx context.Context, job jobs.Job[SyncArgs]) error {
		return s.sync(ctx, job.Args.CustomerID, job.Args.EventID)
	})
	if !s.hasMeter() {
		return
	}
	if s.keys == nil {
		s.logger.Warn("billing: a plan has a meter, and the apikeys store is not set, so no usage is reported. Try: pass billing.WithAPIKeys(keyStore).")
		return
	}
	jobs.Register(w, func(ctx context.Context, _ jobs.Job[UsageArgs]) error {
		_, err := s.reportUsage(ctx)
		return err
	})
	w.Periodic(15*time.Minute, UsageArgs{})
}
