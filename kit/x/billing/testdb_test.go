package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/jobs"
	"github.com/stopyellingatme/spinup/kit/testkit"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe/stripefake"
)

const (
	testSecret   = "whsec_test_one"
	testOrigin   = "https://app.test"
	testPassword = "correct horse battery"
)

var testStart = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

// clock is a settable test clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// billingDB returns a pool on a new database with the auth, apikeys,
// billing (and audit) kit migrations and the River tables.
func billingDB(t *testing.T, withAudit bool) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is empty")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Skipf("PostgreSQL is unavailable: %v", err)
	}
	_ = conn.Close(ctx)
	files := map[string][]string{
		"auth":    {"00002_create_auth.sql", "00005_users_role.sql", "00007_users_auth_version.sql"},
		"apikeys": {"00009_api_keys.sql"},
		"billing": {"00010_billing.sql"},
	}
	if withAudit {
		files["audit"] = []string{"00006_audit_events.sql"}
	}
	fsys := fstest.MapFS{}
	for pack, names := range files {
		for _, name := range names {
			data, err := os.ReadFile("../../../internal/templates/packs/" + pack + "/files/api/db/kit/" + name)
			if err != nil {
				t.Fatal(err)
			}
			fsys[name] = &fstest.MapFile{Data: data, Mode: 0o644}
		}
	}
	pool := testkit.DBWithMigrations(t, fstest.MapFS{}, testkit.WithKitMigrations(fsys))
	hook := jobs.MigrationHook(jobs.WithMigrateOutput(io.Discard))
	if err := hook.AfterUp(t.Context(), testkit.URL(t, pool)); err != nil {
		t.Fatal(err)
	}
	return pool
}

func testRoles() auth.Roles {
	return auth.Roles{Names: []string{"manager", "staff", "pro", "team", "support"}, Admin: "manager", Default: "staff"}
}

func testConfig() Config {
	return Config{DefaultPlan: "free", Plans: []Plan{
		{Name: "free", Features: []string{"basic"}},
		{Name: "pro", Prices: []string{"pro_monthly", "pro_yearly", "pro_gone"}, Features: []string{"basic", "export"}, APIKeysPlan: "pro", Role: "pro", Meter: "api_calls"},
		{Name: "team", Prices: []string{"team_monthly"}, Features: []string{"basic", "export", "team"}, APIKeysPlan: "pro", Role: "team"},
	}}
}

func testKeysConfig() apikeys.Config {
	return apikeys.Config{Prefix: "app", DefaultPlan: "free", MaxKeys: 5, Plans: map[string]apikeys.Plan{
		"free": {PerMinute: 60, PerDay: 1000},
		"pro":  {PerMinute: 600, PerDay: 100000},
	}}
}

func testPrices() []stripe.Price {
	month := &stripe.Recurring{Interval: "month", IntervalCount: 1, UsageType: "licensed"}
	return []stripe.Price{
		{ID: "price_pro_m", LookupKey: "pro_monthly", Currency: "usd", UnitAmount: 1000, Active: true, Recurring: month, Product: stripe.Product{ID: "prod_pro", Name: "Pro"}},
		{ID: "price_pro_y", LookupKey: "pro_yearly", Currency: "usd", UnitAmount: 10000, Active: true, Recurring: &stripe.Recurring{Interval: "year", IntervalCount: 1, UsageType: "licensed"}, Product: stripe.Product{ID: "prod_pro", Name: "Pro"}},
		{ID: "price_team_m", LookupKey: "team_monthly", Currency: "usd", UnitAmount: 5000, Active: true, Recurring: &stripe.Recurring{Interval: "month", IntervalCount: 1, UsageType: "metered", Meter: "mtr_1"}, Product: stripe.Product{ID: "prod_team", Name: "Team"}},
	}
}

// handlerTransport serves requests with a handler, without sockets.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	clock  *clock
	fake   *stripefake.Fake
	client *stripe.Client
	keys   *apikeys.Store
	auth   *auth.Auth
	svc    *Service
	h      http.Handler
	logs   *bytes.Buffer
}

type fixtureOptions struct {
	audit       bool
	noJobs      bool
	noClient    bool
	sessionOnly bool
	cfg         *Config
	opts        []Option
}

func newFixture(t *testing.T, fo fixtureOptions) *fixture {
	t.Helper()
	t.Setenv("AUTH_RATE_LIMIT", "off")
	f := &fixture{t: t, pool: billingDB(t, fo.audit), clock: &clock{now: testStart}, logs: &bytes.Buffer{}}
	f.fake = stripefake.New(stripefake.Options{Secret: testSecret, WebhookURL: "http://api.test" + WebhookPath, Prices: testPrices(),
		Meters: []stripe.Meter{{ID: "mtr_1", EventName: "api_calls", DisplayName: "API calls", Status: "active"}},
		Now:    f.clock.Now, Deliver: &http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.h.ServeHTTP(w, r) })}}})
	var err error
	f.client, err = stripe.New("sk_test_billing", stripe.WithBaseURL("http://stripe.test"), stripe.WithHTTPClient(&http.Client{Transport: f.fake.Transport()}), stripe.WithRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	var storeOpts []apikeys.StoreOption
	if fo.sessionOnly {
		storeOpts = append(storeOpts, apikeys.WithSessionOnly(SessionPrefix))
	}
	f.keys, err = apikeys.NewStore(f.pool, testKeysConfig(), storeOpts...)
	if err != nil {
		t.Fatal(err)
	}
	f.auth, err = auth.New(f.pool, auth.WithSecureCookie(false), auth.WithBearer(f.keys), auth.WithRoles(testRoles()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.auth.Close(context.Background()) })
	cfg := testConfig()
	if fo.cfg != nil {
		cfg = *fo.cfg
	}
	opts := []Option{WithLogger(slog.New(slog.NewTextHandler(f.logs, nil))), WithClock(f.clock.Now), WithAPIKeys(f.keys), WithRoles(testRoles()),
		WithPublicOrigin(testOrigin), WithWebhookSecrets(testSecret)}
	if !fo.noJobs {
		jc, err := jobs.New(f.pool, jobs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		if err != nil {
			t.Fatal(err)
		}
		opts = append(opts, WithJobs(jc))
	}
	opts = append(opts, fo.opts...)
	client := f.client
	if fo.noClient {
		client = nil
	}
	f.svc, err = New(f.pool, cfg, client, opts...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	f.auth.Routes(mux)
	f.svc.Routes(f.auth)(mux)
	f.h = httpx.Chain(mux, f.auth.Middleware(), f.keys.Middleware())
	return f
}

type call struct {
	method, path, body string
	cookie, bearer     string
	header             http.Header
}

func (f *fixture) do(c call) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequestWithContext(f.t.Context(), c.method, "http://api.test"+c.path, strings.NewReader(c.body))
	if c.body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.header {
		r.Header[k] = v
	}
	if c.cookie != "" {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: c.cookie})
	}
	if c.bearer != "" {
		r.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// register signs a user up and returns its ID and session cookie.
func (f *fixture) register(email string) (int64, string) {
	f.t.Helper()
	w := f.do(call{method: "POST", path: "/api/auth/register", body: `{"email":"` + email + `","password":"` + testPassword + `"}`})
	if w.Code != http.StatusCreated {
		f.t.Fatalf("register %s: %d %s", email, w.Code, w.Body)
	}
	var id int64
	if err := f.pool.QueryRow(f.t.Context(), `SELECT id FROM users WHERE email=$1`, email).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			return id, c.Value
		}
	}
	f.t.Fatalf("register %s: no session cookie", email)
	return 0, ""
}

// user inserts a user with the role and returns its ID.
func (f *fixture) user(email, role string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.t.Context(), `INSERT INTO users(email,password_hash,role) VALUES($1,'unused',$2) RETURNING id`, email, role).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// customer creates the Stripe customer of the user through the service.
func (f *fixture) customer(userID int64) string {
	f.t.Helper()
	id, err := f.svc.ensureCustomer(f.t.Context(), userID)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

// subscribe completes a Checkout of the price for the user and returns the subscription ID.
func (f *fixture) subscribe(userID int64, lookupKey string) string {
	f.t.Helper()
	customer := f.customer(userID)
	var priceID string
	metered := false
	for _, p := range testPrices() {
		if p.LookupKey == lookupKey {
			priceID, metered = p.ID, p.Recurring.UsageType == "metered"
		}
	}
	session, err := f.client.CreateCheckoutSession(f.t.Context(), stripe.CheckoutParams{Customer: customer, Price: priceID, ClientReferenceID: strconv.FormatInt(userID, 10),
		SuccessURL: testOrigin + "/settings/billing", CancelURL: testOrigin + "/pricing", Metered: metered}, "")
	if err != nil {
		f.t.Fatal(err)
	}
	sub, err := f.fake.CompleteCheckout(session.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return sub
}

type queued struct {
	id   int64
	args SyncArgs
}

// queuedSyncs returns the sync jobs in insert order.
func (f *fixture) queuedSyncs() []queued {
	f.t.Helper()
	rows, err := f.pool.Query(f.t.Context(), `SELECT id, args FROM river_job WHERE kind=$1 AND state='available' ORDER BY id`, SyncArgs{}.Kind())
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (queued, error) {
		var q queued
		var raw []byte
		if err := row.Scan(&q.id, &raw); err != nil {
			return q, err
		}
		return q, json.Unmarshal(raw, &q.args)
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// work runs the jobs as the worker does and marks them completed.
func (f *fixture) work(list []queued) {
	f.t.Helper()
	for _, q := range list {
		if err := f.svc.sync(f.t.Context(), q.args.CustomerID, q.args.EventID); err != nil {
			f.t.Fatal(err)
		}
		if _, err := f.pool.Exec(f.t.Context(), `UPDATE river_job SET state='completed', finalized_at=now() WHERE id=$1`, q.id); err != nil {
			f.t.Fatal(err)
		}
	}
}

// drain runs every queued sync job.
func (f *fixture) drain() { f.t.Helper(); f.work(f.queuedSyncs()) }

func (f *fixture) entitlement(userID int64) Entitlement {
	f.t.Helper()
	e, err := f.svc.Entitlement(f.t.Context(), userID)
	if err != nil {
		f.t.Fatal(err)
	}
	return e
}

func (f *fixture) role(userID int64) string {
	f.t.Helper()
	var role string
	if err := f.pool.QueryRow(f.t.Context(), `SELECT role FROM users WHERE id=$1`, userID).Scan(&role); err != nil {
		f.t.Fatal(err)
	}
	return role
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.t.Context(), query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body, err)
	}
	return out
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body)
	}
	if status >= 400 && !strings.Contains(w.Body.String(), "Try:") {
		t.Errorf("the %d answer has no Try: line: %s", status, w.Body)
	}
}
