package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

// addUsage adds count calls of the key on the day.
func (f *fixture) addUsage(keyID int64, day time.Time, count int64) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.t.Context(), `INSERT INTO api_key_usage(key_id,day,count) VALUES($1,$2::date,$3)
ON CONFLICT (key_id,day) DO UPDATE SET count=api_key_usage.count+excluded.count`, keyID, day.Format(time.DateOnly), count); err != nil {
		f.t.Fatal(err)
	}
}

func TestReportUsage(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	uid := f.user("meter@example.com", "")
	f.subscribe(uid, "pro_monthly")
	f.drain()
	customer := f.customer(uid)
	k1, _, err := f.keys.Create(t.Context(), uid, apikeysInput())
	if err != nil {
		t.Fatal(err)
	}
	k2, _, err := f.keys.Create(t.Context(), uid, apikeysInput())
	if err != nil {
		t.Fatal(err)
	}
	// A free user with usage and a customer sends nothing.
	free := f.user("nometer@example.com", "")
	f.customer(free)
	k3, _, _ := f.keys.Create(t.Context(), free, apikeysInput())
	today := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	f.addUsage(k1.ID, yesterday, 5)
	f.addUsage(k2.ID, yesterday, 2)
	f.addUsage(k1.ID, today, 3)
	f.addUsage(k3.ID, today, 50)
	f.addUsage(k1.ID, today.AddDate(0, 0, -30), 9) // outside the 30 days
	if err := f.svc.ReportUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := f.fake.MeterEvents()
	want := []stripe.MeterEvent{
		{EventName: "api_calls", Customer: customer, Identifier: fmt.Sprintf("apikeys:%d:2026-11-06:7", uid), Value: 7, Timestamp: time.Date(2026, 11, 6, 23, 59, 59, 0, time.UTC)},
		{EventName: "api_calls", Customer: customer, Identifier: fmt.Sprintf("apikeys:%d:2026-11-07:3", uid), Value: 3, Timestamp: testStart},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v", got)
	}
	for i := range want {
		if got[i].EventName != want[i].EventName || got[i].Customer != want[i].Customer || got[i].Identifier != want[i].Identifier || got[i].Value != want[i].Value || !got[i].Timestamp.Equal(want[i].Timestamp) {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// A second pass sends nothing.
	before := f.requests("/v1/billing/meter_events")
	if err := f.svc.ReportUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := f.requests("/v1/billing/meter_events"); n != before {
		t.Fatalf("a second pass sent %d events", n-before)
	}
	// New usage today sends the delta only.
	f.addUsage(k2.ID, today, 4)
	f.clock.Add(time.Hour)
	if err := f.svc.ReportUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	got = f.fake.MeterEvents()
	if last := got[len(got)-1]; len(got) != 3 || last.Value != 4 || last.Identifier != fmt.Sprintf("apikeys:%d:2026-11-07:7", uid) || !last.Timestamp.Equal(testStart.Add(time.Hour)) {
		t.Fatalf("delta events = %+v", got)
	}
	// A failed send fails the pass, records nothing, and the retry sends it.
	f.addUsage(k1.ID, today, 1)
	f.fake.FailNext("/v1/billing/meter_events", 500, "false")
	err = f.svc.ReportUsage(t.Context())
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("failed pass = %v", err)
	}
	var reported int64
	if err := f.pool.QueryRow(t.Context(), `SELECT reported FROM billing_usage_reports WHERE user_id=$1 AND day='2026-11-07'`, uid).Scan(&reported); err != nil || reported != 7 {
		t.Fatalf("reported = %d, %v; want 7", reported, err)
	}
	if err := f.svc.ReportUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	got = f.fake.MeterEvents()
	if last := got[len(got)-1]; len(got) != 4 || last.Value != 1 || last.Identifier != fmt.Sprintf("apikeys:%d:2026-11-07:8", uid) {
		t.Fatalf("retry events = %+v", got)
	}
	// A pass that stopped after a send and before its checkpoint left a pending
	// target. The next pass sends that event again first (Stripe dedupes it by
	// its identifier), and then the new delta.
	f.addUsage(k1.ID, today, 2)
	if _, err := f.pool.Exec(t.Context(), `UPDATE billing_usage_reports SET pending=9 WHERE user_id=$1 AND day='2026-11-07'`, uid); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReportUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	got = f.fake.MeterEvents()
	if len(got) != 6 || got[4].Identifier != fmt.Sprintf("apikeys:%d:2026-11-07:9", uid) || got[4].Value != 1 ||
		got[5].Identifier != fmt.Sprintf("apikeys:%d:2026-11-07:10", uid) || got[5].Value != 1 {
		t.Fatalf("pending events = %+v", got)
	}
	// Another pass that holds the lock of the user: this pass sends nothing.
	f.addUsage(k1.ID, today, 5)
	conn, err := f.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(t.Context(), `SELECT pg_advisory_lock(hashtext('billing-usage:' || $1::bigint))`, uid); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReportUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.fake.MeterEvents()); n != 6 {
		t.Fatalf("a locked user got %d events, want 6", n)
	}
	if _, err := conn.Exec(t.Context(), `SELECT pg_advisory_unlock(hashtext('billing-usage:' || $1::bigint))`, uid); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReportUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got = f.fake.MeterEvents(); len(got) != 7 || got[6].Value != 5 {
		t.Fatalf("after the unlock = %+v", got)
	}
}

type failingReporter struct{ calls int }

func (r *failingReporter) Report(context.Context, stripe.MeterEvent) error {
	r.calls++
	return errors.New("down")
}

func TestReportUsageStopsTheUser(t *testing.T) {
	rep := &failingReporter{}
	f := newFixture(t, fixtureOptions{opts: []Option{WithReporter(rep)}})
	uid := f.user("stop@example.com", "")
	f.subscribe(uid, "pro_monthly")
	f.drain()
	k, _, _ := f.keys.Create(t.Context(), uid, apikeysInput())
	f.addUsage(k.ID, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), 1)
	f.addUsage(k.ID, time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), 1)
	err := f.svc.ReportUsage(t.Context())
	if err == nil || !strings.Contains(err.Error(), "Try:") || rep.calls != 1 {
		t.Fatalf("err %v after %d calls, want one call", err, rep.calls)
	}
}
