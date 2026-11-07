package billing

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func (f *fixture) command(args ...string) (string, error) {
	f.t.Helper()
	var out bytes.Buffer
	err := f.svc.runCommand(f.t.Context(), args, &out)
	return out.String(), err
}

func TestCommandCheck(t *testing.T) {
	cfg := testConfig()
	cfg.Plans[1].Prices = []string{"pro_monthly", "pro_yearly"}
	f := newFixture(t, fixtureOptions{cfg: &cfg})
	out, err := f.command("check")
	if err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	want := "ok    Stripe key\n" +
		"ok    price pro_monthly (price_pro_m, every 1 month, licensed)\n" +
		"ok    price pro_yearly (price_pro_y, every 1 year, licensed)\n" +
		"ok    price team_monthly (price_team_m, every 1 month, metered)\n" +
		"ok    meter api_calls (mtr_1)\n" +
		"ok    webhook secret (1 set)\n"
	if out != want {
		t.Fatalf("output =\n%s\nwant\n%s", out, want)
	}
}

func TestCommandCheckFailures(t *testing.T) {
	cfg := testConfig()
	cfg.Plans[2].Meter = "gone_meter"
	f := newFixture(t, fixtureOptions{cfg: &cfg, opts: []Option{}})
	f.svc.secrets = nil
	f.fake.FailNext("/v1/balance", 401, "false")
	out, err := f.command("check")
	if err == nil || !strings.Contains(err.Error(), "4 checks failed") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("err = %v", err)
	}
	for _, line := range []string{"FAIL  Stripe key: stripe GET /v1/balance failed (status 401)", "FAIL  price pro_gone: no active price has this lookup key",
		"FAIL  meter gone_meter: no meter has this event name", "FAIL  webhook secret: not set", "ok    price pro_monthly"} {
		if !strings.Contains(out, line) {
			t.Errorf("output has no %q:\n%s", line, out)
		}
	}
	if strings.Contains(out, "sk_test_billing") {
		t.Fatal("the output prints the key")
	}
}

func TestCommandStatusSyncReport(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	uid := f.user("ops@example.com", "")
	out, err := f.command("status", "OPS@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("user\tops@example.com\t%d\ncustomer\tnone\nentitlement\tfree\tstatus=-\tfeatures=basic\n", uid); out != want {
		t.Fatalf("status =\n%s\nwant\n%s", out, want)
	}
	if _, err := f.command("sync", "ops@example.com"); err == nil || !strings.Contains(err.Error(), "no Stripe customer") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("sync without a customer: %v", err)
	}
	sub := f.subscribe(uid, "pro_monthly")
	customer := f.customer(uid)
	out, err = f.command("sync", "ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if want := "synced ops@example.com\t" + customer + "\tplan=pro\n"; out != want {
		t.Fatalf("sync = %q, want %q", out, want)
	}
	out, err = f.command("status", "ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	end := testStart.Add(30 * 24 * time.Hour).Format(time.RFC3339)
	want := fmt.Sprintf("user\tops@example.com\t%d\ncustomer\t%s\nsubscription\t%s\tactive\tpro\tpro_monthly\tperiod_end=%s\tcancel_at_period_end=false\nentitlement\tpro\tstatus=active\tfeatures=basic,export\n", uid, customer, sub, end)
	if out != want {
		t.Fatalf("status =\n%s\nwant\n%s", out, want)
	}
	k, _, _ := f.keys.Create(t.Context(), uid, apikeysInput())
	f.addUsage(k.ID, time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), 2)
	f.addUsage(k.ID, time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), 2)
	if out, err = f.command("report-usage"); err != nil || out != "sent 2 meter events\n" {
		t.Fatalf("report-usage = %q, %v", out, err)
	}
	if out, err = f.command("report-usage"); err != nil || out != "sent 0 meter events\n" {
		t.Fatalf("report-usage = %q, %v", out, err)
	}
}

func TestCommandUsage(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	for _, args := range [][]string{{}, {"nope"}, {"status"}, {"sync"}, {"status", "a@b.c", "extra"}, {"check", "extra"}} {
		if _, err := f.command(args...); err == nil || !strings.Contains(err.Error(), "Try: "+commandUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
	if _, err := f.command("status", "missing@example.com"); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Errorf("status of a missing user: %v", err)
	}
	if f.svc.Command() == nil {
		t.Fatal("Command returned nil")
	}
}
