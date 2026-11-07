package scripttest

import (
	"fmt"
	"strings"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe/stripefake"
)

// stripeKey is the testscript value key of the Stripe fake of one script.
type stripeKey struct{}

// stripeState holds the Stripe fake of one script. Setup stores one for each script.
type stripeState struct{ fake *stripefake.Fake }

// stripeCatalog is the price catalog of the script fake: the licensed prices free and
// pro_monthly, and the metered price pro_metered on the meter api_calls.
func stripeCatalog() ([]stripe.Price, []stripe.Meter) {
	free := stripe.Product{ID: "prod_free", Name: "Free"}
	pro := stripe.Product{ID: "prod_pro", Name: "Pro"}
	licensed := func(id, key string, amount int64, product stripe.Product) stripe.Price {
		return stripe.Price{ID: id, LookupKey: key, Currency: "usd", UnitAmount: amount, Active: true, Product: product,
			Recurring: &stripe.Recurring{Interval: "month", IntervalCount: 1, UsageType: "licensed"}}
	}
	prices := []stripe.Price{
		licensed("price_free", "free", 0, free),
		licensed("price_pro_monthly", "pro_monthly", 1500, pro),
		{ID: "price_pro_metered", LookupKey: "pro_metered", Currency: "usd", UnitAmount: 1, Active: true, Product: pro,
			Recurring: &stripe.Recurring{Interval: "month", IntervalCount: 1, UsageType: "metered", Meter: "mtr_api_calls"}},
	}
	meters := []stripe.Meter{{ID: "mtr_api_calls", EventName: "api_calls", DisplayName: "API calls", Status: "active"}}
	return prices, meters
}

const stripeUsage = "usage: stripefake start <secret> <webhook-url> | complete <session-id> | status <sub-id> <status> | resend <event-id> | meter-events | events"

// stripeFakeCmd is the script command stripefake. See the package doc comment.
func stripeFakeCmd(ts *testscript.TestScript, neg bool, args []string) {
	state, _ := ts.Value(stripeKey{}).(*stripeState)
	if state == nil || len(args) == 0 {
		ts.Fatalf("%s", stripeUsage)
	}
	if args[0] != "start" && state.fake == nil {
		ts.Fatalf("stripefake %s: the fake is not running\nTry: run stripefake start <secret> <webhook-url> first", args[0])
	}
	// result handles the commands that can fail on purpose, such as a webhook that answers 400.
	result := func(err error) {
		switch {
		case err != nil && !neg:
			ts.Fatalf("stripefake %s: %v", args[0], err)
		case err == nil && neg:
			ts.Fatalf("stripefake %s: unexpected success", args[0])
		case err != nil:
			ts.Logf("stripefake %s: %v", args[0], err)
		}
	}
	switch {
	case args[0] == "start" && len(args) == 3 && !neg:
		if state.fake != nil {
			ts.Fatalf("stripefake start: the fake is already running\nTry: start the fake once in each script")
		}
		prices, meters := stripeCatalog()
		fake := stripefake.New(stripefake.Options{Secret: args[1], WebhookURL: args[2], Prices: prices, Meters: meters})
		url, err := fake.Start()
		if err != nil {
			ts.Fatalf("stripefake start: %v", err)
		}
		state.fake = fake
		ts.Defer(fake.Close)
		ts.Setenv("STRIPE_API_BASE", url)
		ts.Setenv("STRIPE_SECRET_KEY", "sk_test_fake")
		ts.Setenv("STRIPE_WEBHOOK_SECRET", args[1])
	case args[0] == "complete" && len(args) == 2:
		sub, err := state.fake.CompleteCheckout(args[1])
		if sub != "" {
			_, _ = fmt.Fprintln(ts.Stdout(), sub)
		}
		result(err)
	case args[0] == "status" && len(args) == 3:
		result(state.fake.SetStatus(args[1], args[2]))
	case args[0] == "resend" && len(args) == 2:
		result(state.fake.Resend(args[1]))
	case args[0] == "meter-events" && len(args) == 1 && !neg:
		for _, e := range state.fake.MeterEvents() {
			_, _ = fmt.Fprintf(ts.Stdout(), "%s %s %s %d\n", e.EventName, e.Customer, e.Identifier, e.Value)
		}
	case args[0] == "events" && len(args) == 1 && !neg:
		for _, e := range state.fake.Events() {
			_, _ = fmt.Fprintf(ts.Stdout(), "%s %s %s\n", e.ID, e.Type, strings.TrimSpace(e.Customer()))
		}
	default:
		ts.Fatalf("%s", stripeUsage)
	}
}
