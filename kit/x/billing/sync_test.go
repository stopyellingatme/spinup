package billing

import (
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/jobs"
)

func TestSyncSubscribeAndCancel(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	uid := f.user("pro@example.com", "")
	if e := f.entitlement(uid); e.Plan != "free" || !slices.Equal(e.Features, []string{"basic"}) || e.Status != "" || e.HasCustomer {
		t.Fatalf("before = %+v", e)
	}
	sub := f.subscribe(uid, "pro_monthly")
	f.drain()
	e := f.entitlement(uid)
	if e.Plan != "pro" || e.Status != "active" || !slices.Equal(e.Features, []string{"basic", "export"}) || !e.HasCustomer {
		t.Fatalf("after checkout = %+v", e)
	}
	if want := testStart.Add(30 * 24 * time.Hour); !e.CurrentPeriodEnd.Equal(want) {
		t.Fatalf("CurrentPeriodEnd = %s, want %s", e.CurrentPeriodEnd, want)
	}
	if r := f.role(uid); r != "pro" {
		t.Fatalf("role = %q, want pro", r)
	}
	if plan, _, _ := f.keys.PlanOf(t.Context(), uid); plan != "pro" {
		t.Fatalf("apikeys plan = %q, want pro", plan)
	}
	if n := f.count(`SELECT count(*) FROM billing_events WHERE processed_at IS NOT NULL AND error=''`); n != 2 {
		t.Fatalf("%d processed events, want 2", n)
	}
	var lookup, plan string
	if err := f.pool.QueryRow(t.Context(), `SELECT price_lookup_key, plan FROM billing_subscriptions WHERE id=$1`, sub).Scan(&lookup, &plan); err != nil {
		t.Fatal(err)
	}
	if lookup != "pro_monthly" || plan != "pro" {
		t.Fatalf("subscription row: %q %q", lookup, plan)
	}
	if err := f.fake.SetStatus(sub, "canceled"); err != nil {
		t.Fatal(err)
	}
	f.drain()
	if e := f.entitlement(uid); e.Plan != "free" || e.Status != "canceled" || !e.CurrentPeriodEnd.IsZero() {
		t.Fatalf("after cancel = %+v", e)
	}
	if r := f.role(uid); r != "staff" {
		t.Fatalf("role after cancel = %q, want the default role staff", r)
	}
	if plan, _, _ := f.keys.PlanOf(t.Context(), uid); plan != "free" {
		t.Fatalf("apikeys plan after cancel = %q, want the default free", plan)
	}
}

func TestSyncStatuses(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	want := map[string]string{"active": "pro", "trialing": "pro", "past_due": "pro", "unpaid": "free", "paused": "free", "canceled": "free"}
	for _, status := range []string{"past_due", "active", "unpaid", "trialing", "paused", "canceled"} {
		uid := f.user(status+"@example.com", "")
		sub := f.subscribe(uid, "pro_yearly")
		if err := f.fake.SetStatus(sub, status); err != nil {
			t.Fatal(err)
		}
		f.drain()
		e := f.entitlement(uid)
		if e.Plan != want[status] {
			t.Errorf("status %s: plan %q, want %q", status, e.Plan, want[status])
		}
		if e.Plan == "pro" && e.Status != status {
			t.Errorf("status %s: entitlement status %q", status, e.Status)
		}
	}
}

func TestSyncRanks(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	uid := f.user("ranks@example.com", "")
	pro := f.subscribe(uid, "pro_monthly")
	team := f.subscribe(uid, "team_monthly")
	f.drain()
	if e := f.entitlement(uid); e.Plan != "team" || !slices.Contains(e.Features, "team") {
		t.Fatalf("two subscriptions = %+v, want the higher plan team", e)
	}
	if r := f.role(uid); r != "team" {
		t.Fatalf("role = %q, want team", r)
	}
	// A billing-granted role changes to the role of the new plan.
	if err := f.fake.SetStatus(team, "unpaid"); err != nil {
		t.Fatal(err)
	}
	f.drain()
	if e := f.entitlement(uid); e.Plan != "pro" {
		t.Fatalf("after team unpaid = %+v, want pro", e)
	}
	if r := f.role(uid); r != "pro" {
		t.Fatalf("role = %q, want pro", r)
	}
	if err := f.fake.SetStatus(pro, "canceled"); err != nil {
		t.Fatal(err)
	}
	f.drain()
	if e := f.entitlement(uid); e.Plan != "free" {
		t.Fatalf("after both ended = %+v", e)
	}
	if n := f.count(`SELECT count(*) FROM billing_subscriptions WHERE user_id=$1`, uid); n != 2 {
		t.Fatalf("%d subscription rows, want 2", n)
	}
}

func TestSyncRoleRules(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	for _, tc := range []struct{ role, during, after string }{
		{"", "pro", "staff"},
		{"staff", "pro", "staff"},
		{"manager", "manager", "manager"},
		{"support", "support", "support"},
	} {
		uid := f.user("role-"+tc.role+"@example.com", tc.role)
		sub := f.subscribe(uid, "pro_monthly")
		f.drain()
		if r := f.role(uid); r != tc.during {
			t.Errorf("role %q: during the subscription %q, want %q", tc.role, r, tc.during)
		}
		if e := f.entitlement(uid); e.Plan != "pro" {
			t.Errorf("role %q: plan %q, want pro", tc.role, e.Plan)
		}
		if err := f.fake.SetStatus(sub, "canceled"); err != nil {
			t.Fatal(err)
		}
		f.drain()
		if r := f.role(uid); r != tc.after {
			t.Errorf("role %q: after the cancel %q, want %q", tc.role, r, tc.after)
		}
	}
}

func TestSyncWithoutAPIKeysOrRoles(t *testing.T) {
	cfg := Config{DefaultPlan: "free", Plans: []Plan{{Name: "free"}, {Name: "pro", Prices: []string{"pro_monthly"}, Features: []string{"export"}}}}
	f := newFixture(t, fixtureOptions{cfg: &cfg})
	plain, err := New(f.pool, cfg, f.client, WithPublicOrigin(testOrigin), WithClock(f.clock.Now), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	f.svc = plain
	uid := f.user("plain@example.com", "")
	f.subscribe(uid, "pro_monthly")
	f.drain()
	if e := f.entitlement(uid); e.Plan != "pro" {
		t.Fatalf("plan = %+v", e)
	}
	if r := f.role(uid); r != "" {
		t.Fatalf("role = %q, want unchanged", r)
	}
	if n := f.count(`SELECT count(*) FROM api_key_plans`); n != 0 {
		t.Fatalf("api_key_plans has %d rows without WithAPIKeys", n)
	}
}

func TestSyncEventOrder(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	for _, reverse := range []bool{false, true} {
		uid := f.user("order-"+strconv.FormatBool(reverse)+"@example.com", "")
		sub := f.subscribe(uid, "pro_monthly")
		if err := f.fake.SetStatus(sub, "canceled"); err != nil {
			t.Fatal(err)
		}
		list := f.queuedSyncs()
		if len(list) != 3 {
			t.Fatalf("%d queued syncs, want 3", len(list))
		}
		if reverse {
			slices.Reverse(list)
		}
		f.work(list)
		if e := f.entitlement(uid); e.Plan != "free" || e.Status != "canceled" {
			t.Errorf("reverse=%t: %+v, want free and canceled", reverse, e)
		}
		// Each sync reads the final state from Stripe, so the plan never changed.
		if r := f.role(uid); r != "" {
			t.Errorf("reverse=%t: role %q, want unchanged", reverse, r)
		}
		if n := f.count(`SELECT count(*) FROM billing_events WHERE customer_id=$1 AND processed_at IS NOT NULL`, list[0].args.CustomerID); n != 3 {
			t.Errorf("reverse=%t: %d processed events, want 3", reverse, n)
		}
	}
}

func TestSyncUnknownCustomer(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	body := eventBody(t, "evt_unknown", "invoice.paid", "cus_nobody")
	expect(t, f.post(body, signed(body)), 200)
	f.drain()
	var errText string
	if err := f.pool.QueryRow(t.Context(), `SELECT error FROM billing_events WHERE event_id='evt_unknown' AND processed_at IS NOT NULL`).Scan(&errText); err != nil {
		t.Fatal(err)
	}
	if errText != "unknown customer" {
		t.Fatalf("error = %q", errText)
	}
	if err := f.svc.Sync(t.Context(), "cus_nobody"); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("Sync of an unknown customer = %v", err)
	}
	if !strings.Contains(f.logs.String(), "level=WARN") {
		t.Errorf("no warning: %s", f.logs)
	}
}

func TestSyncStripeError(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	uid := f.user("fail@example.com", "")
	f.subscribe(uid, "pro_monthly")
	list := f.queuedSyncs()
	f.fake.FailNext("/v1/subscriptions", 500, "false")
	err := f.svc.sync(t.Context(), list[0].args.CustomerID, list[0].args.EventID)
	if err == nil || !strings.Contains(err.Error(), "Try:") || strings.Contains(err.Error(), "sk_test_billing") {
		t.Fatalf("sync error = %v", err)
	}
	var errText string
	if err := f.pool.QueryRow(t.Context(), `SELECT error FROM billing_events WHERE event_id=$1 AND processed_at IS NULL`, list[0].args.EventID).Scan(&errText); err != nil || errText == "" {
		t.Fatalf("event error %q, %v", errText, err)
	}
	if e := f.entitlement(uid); e.Plan != "free" {
		t.Fatalf("a failed sync changed the plan: %+v", e)
	}
	f.work(list)
	if e := f.entitlement(uid); e.Plan != "pro" {
		t.Fatalf("the retry did not apply: %+v", e)
	}
}

func TestSyncAudit(t *testing.T) {
	f := newFixture(t, fixtureOptions{audit: true})
	uid := f.user("audit@example.com", "")
	sub := f.subscribe(uid, "pro_monthly")
	f.drain()
	if err := f.fake.SetStatus(sub, "canceled"); err != nil {
		t.Fatal(err)
	}
	f.drain()
	events, err := audit.List(t.Context(), f.pool, audit.Filter{Table: "billing_entitlements", Action: "billing.plan_changed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("%d plan_changed events, want 2: %+v", len(events), events)
	}
	latest := events[0]
	var changes map[string]map[string]any
	if err := json.Unmarshal(latest.Changes, &changes); err != nil {
		t.Fatal(err)
	}
	if latest.RecordID != strconv.FormatInt(uid, 10) || latest.Actor.Kind != audit.KindSystem || changes["plan"]["old"] != "pro" || changes["plan"]["new"] != "free" {
		t.Fatalf("event = %+v, changes %v", latest, changes)
	}
	if n := f.count(`SELECT count(*) FROM audit_events WHERE table_name IN ('billing_customers','billing_subscriptions','billing_entitlements','users','api_key_plans') AND action <> 'billing.plan_changed'`); n == 0 {
		t.Fatal("the triggers wrote no row changes")
	}
	if n := f.count(`SELECT count(*) FROM audit_events WHERE table_name IN ('billing_events','billing_usage_reports')`); n != 0 {
		t.Fatalf("%d audit rows for the unaudited tables", n)
	}
	if n := f.count(`SELECT count(*) FROM audit_events WHERE table_name='billing_entitlements' AND actor_kind <> 'system'`); n != 0 {
		t.Fatalf("%d entitlement changes without the system actor", n)
	}
	// A repeated sync with no change writes no audit row.
	before := f.count(`SELECT count(*) FROM audit_events`)
	if err := f.svc.Sync(t.Context(), f.customer(uid)); err != nil {
		t.Fatal(err)
	}
	if after := f.count(`SELECT count(*) FROM audit_events`); after != before {
		t.Fatalf("a sync with no change wrote %d audit rows", after-before)
	}
}

func TestRegister(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	w, err := jobs.NewWorker(f.pool, jobs.WithPollInterval(50*time.Millisecond), jobs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Register(w)
	if got := w.Kinds(); !slices.Equal(got, []string{"billing.report_usage", "billing.sync_customer"}) {
		t.Fatalf("kinds = %v", got)
	}
	uid := f.user("worker@example.com", "")
	f.subscribe(uid, "pro_monthly")
	if err := w.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(t.Context()) })
	deadline := time.Now().Add(20 * time.Second)
	for f.entitlement(uid).Plan != "pro" {
		if time.Now().After(deadline) {
			t.Fatal("the worker did not run the sync job")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Without a meter or without WithAPIKeys, only the sync job is registered.
	plain, err := New(f.pool, Config{DefaultPlan: "free", Plans: []Plan{{Name: "free"}}}, f.client, WithPublicOrigin(testOrigin))
	if err != nil {
		t.Fatal(err)
	}
	w2, _ := jobs.NewWorker(f.pool)
	plain.Register(w2)
	if got := w2.Kinds(); !slices.Equal(got, []string{"billing.sync_customer"}) {
		t.Fatalf("kinds = %v", got)
	}
	noKeys, err := New(f.pool, testConfig(), f.client, WithPublicOrigin(testOrigin), WithRoles(testRoles()), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	w3, _ := jobs.NewWorker(f.pool)
	noKeys.Register(w3)
	if got := w3.Kinds(); !slices.Equal(got, []string{"billing.sync_customer"}) {
		t.Fatalf("kinds = %v", got)
	}
}
