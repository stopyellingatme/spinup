package billing

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe/stripefake"
)

func (f *fixture) requests(path string) int {
	n := 0
	for _, r := range f.fake.Requests() {
		if r.Path == path {
			n++
		}
	}
	return n
}

func TestGetBilling(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	if w := f.do(call{method: "GET", path: SessionPrefix}); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
	uid, cookie := f.register("get@example.com")
	w := f.do(call{method: "GET", path: SessionPrefix, cookie: cookie})
	expect(t, w, http.StatusOK)
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"plan":"free"`) || !strings.Contains(body, `"features":["basic"]`) || !strings.Contains(body, `"status":""`) ||
		!strings.Contains(body, `"current_period_end":null`) || !strings.Contains(body, `"cancel_at_period_end":false`) || !strings.Contains(body, `"has_customer":false`) {
		t.Fatalf("body = %s", body)
	}
	f.subscribe(uid, "pro_monthly")
	f.drain()
	out := decode(t, f.do(call{method: "GET", path: SessionPrefix, cookie: cookie}))
	if out["plan"] != "pro" || out["status"] != "active" || out["has_customer"] != true || out["current_period_end"] != testStart.Add(30*24*time.Hour).Format(time.RFC3339) {
		t.Fatalf("body = %v", out)
	}
}

func TestCheckout(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	uid, cookie := f.register("buy@example.com")
	checkout := func(body string) *httptest.ResponseRecorder {
		return f.do(call{method: "POST", path: SessionPrefix + "/checkout", body: body, cookie: cookie})
	}
	expect(t, checkout(`{"price":"gold_monthly"}`), http.StatusBadRequest)
	expect(t, checkout(`{"price":""}`), http.StatusBadRequest)
	expect(t, checkout(`{}`), http.StatusUnprocessableEntity)
	expect(t, checkout(`{"price":"pro_monthly","extra":1}`), http.StatusBadRequest)
	expect(t, checkout(`not json`), http.StatusBadRequest)
	if n := len(f.fake.Requests()); n != 0 {
		t.Fatalf("a rejected checkout made %d Stripe calls", n)
	}
	w := checkout(`{"price":"pro_monthly"}`)
	expect(t, w, http.StatusOK)
	url, _ := decode(t, w)["url"].(string)
	if !strings.HasPrefix(url, "https://checkout.stripe.fake/") {
		t.Fatalf("url = %q", url)
	}
	user := strconv.FormatInt(uid, 10)
	var customerReq, sessionReq *stripefake.Request
	for _, r := range f.fake.Requests() {
		switch r.Path {
		case "/v1/customers":
			customerReq = &r
		case "/v1/checkout/sessions":
			sessionReq = &r
		}
	}
	if customerReq == nil || customerReq.Headers.Get("Idempotency-Key") != "customer:"+user || customerReq.Form.Get("metadata[user_id]") != user || customerReq.Form.Get("email") != "buy@example.com" {
		t.Fatalf("customer request = %+v", customerReq)
	}
	form := sessionReq.Form
	if form.Get("client_reference_id") != user || form.Get("metadata[user_id]") != user || form.Get("line_items[0][price]") != "price_pro_m" ||
		form.Get("success_url") != testOrigin+"/settings/billing?session_id={CHECKOUT_SESSION_ID}" || form.Get("cancel_url") != testOrigin+"/pricing" {
		t.Fatalf("session form = %v", form)
	}
	if !f.entitlement(uid).HasCustomer {
		t.Fatal("the checkout did not store the customer")
	}
	// A second checkout reuses the customer.
	expect(t, checkout(`{"price":"pro_yearly"}`), http.StatusOK)
	if n := f.requests("/v1/customers"); n != 1 {
		t.Fatalf("%d customer creations, want 1", n)
	}
	// A plan of metered prices only is bought through its metered price, with no quantity.
	expect(t, checkout(`{"price":"team_monthly"}`), http.StatusOK)
	// A declared price that Stripe does not have is unavailable.
	expect(t, checkout(`{"price":"pro_gone"}`), http.StatusServiceUnavailable)
	// A live subscription answers 409.
	f.subscribe(uid, "pro_monthly")
	f.drain()
	w = checkout(`{"price":"team_monthly"}`)
	expect(t, w, http.StatusConflict)
	if !strings.Contains(w.Body.String(), "Try: open the billing portal to change the plan.") {
		t.Fatalf("body = %s", w.Body)
	}
}

func TestCheckoutOptions(t *testing.T) {
	cfg := testConfig()
	cfg.TrialDays, cfg.AutomaticTax, cfg.AllowPromotionCodes = 14, true, true
	f := newFixture(t, fixtureOptions{cfg: &cfg})
	_, cookie := f.register("trial@example.com")
	expect(t, f.do(call{method: "POST", path: SessionPrefix + "/checkout", body: `{"price":"pro_monthly"}`, cookie: cookie}), http.StatusOK)
	for _, r := range f.fake.Requests() {
		if r.Path == "/v1/checkout/sessions" {
			if r.Form.Get("subscription_data[trial_period_days]") != "14" || r.Form.Get("automatic_tax[enabled]") != "true" || r.Form.Get("allow_promotion_codes") != "true" {
				t.Fatalf("form = %v", r.Form)
			}
			return
		}
	}
	t.Fatal("no checkout request")
}

func TestPortal(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	uid, cookie := f.register("portal@example.com")
	w := f.do(call{method: "POST", path: SessionPrefix + "/portal", body: `{}`, cookie: cookie})
	expect(t, w, http.StatusConflict)
	f.customer(uid)
	w = f.do(call{method: "POST", path: SessionPrefix + "/portal", body: `{}`, cookie: cookie})
	expect(t, w, http.StatusOK)
	if url, _ := decode(t, w)["url"].(string); !strings.HasPrefix(url, "https://billing.stripe.fake/") {
		t.Fatalf("url = %q", url)
	}
	for _, r := range f.fake.Requests() {
		if r.Path == "/v1/billing_portal/sessions" && r.Form.Get("return_url") != testOrigin+"/settings/billing" {
			t.Fatalf("return_url = %q", r.Form.Get("return_url"))
		}
	}
	expect(t, f.do(call{method: "POST", path: SessionPrefix + "/portal", body: `{"x":1}`, cookie: cookie}), http.StatusBadRequest)
}

func TestSyncRoute(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	uid, cookie := f.register("sync@example.com")
	other, otherCookie := f.register("other@example.com")
	syncCall := func(cookie, id string) *httptest.ResponseRecorder {
		return f.do(call{method: "POST", path: SessionPrefix + "/sync", body: `{"session_id":"` + id + `"}`, cookie: cookie})
	}
	// No customer yet, an unknown session, and a malformed ID answer 404.
	expect(t, syncCall(cookie, "cs_test_404"), http.StatusNotFound)
	f.customer(uid)
	expect(t, syncCall(cookie, "cs_test_404"), http.StatusNotFound)
	expect(t, syncCall(cookie, "pi_123"), http.StatusNotFound)
	w := f.do(call{method: "POST", path: SessionPrefix + "/checkout", body: `{"price":"pro_monthly"}`, cookie: cookie})
	expect(t, w, http.StatusOK)
	url, _ := decode(t, w)["url"].(string)
	session := url[strings.LastIndex(url, "/")+1:]
	// The webhook is down, so only the success page sync applies the plan.
	if _, err := f.fake.CompleteCheckout(session); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM river_job`); err != nil {
		t.Fatal(err)
	}
	// Another user's session is not found, with or without a customer.
	expect(t, syncCall(otherCookie, session), http.StatusNotFound)
	f.customer(other)
	expect(t, syncCall(otherCookie, session), http.StatusNotFound)
	w = syncCall(cookie, session)
	expect(t, w, http.StatusOK)
	if out := decode(t, w); out["plan"] != "pro" || out["status"] != "active" || out["has_customer"] != true {
		t.Fatalf("body = %v", out)
	}
	if e := f.entitlement(other); e.Plan != "free" {
		t.Fatalf("the other user got %q", e.Plan)
	}
	expect(t, f.do(call{method: "POST", path: SessionPrefix + "/sync", body: `{}`, cookie: cookie}), http.StatusUnprocessableEntity)
}

func TestSessionRoutesRejectAPIKeys(t *testing.T) {
	for _, sessionOnly := range []bool{false, true} {
		f := newFixture(t, fixtureOptions{sessionOnly: sessionOnly})
		uid, _ := f.register("key" + strconv.FormatBool(sessionOnly) + "@example.com")
		_, token, err := f.keys.Create(t.Context(), uid, apikeysInput())
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []call{
			{method: "GET", path: SessionPrefix},
			{method: "POST", path: SessionPrefix + "/checkout", body: `{"price":"pro_monthly"}`},
			{method: "POST", path: SessionPrefix + "/portal", body: `{}`},
			{method: "POST", path: SessionPrefix + "/sync", body: `{"session_id":"cs_test_1"}`},
		} {
			c.bearer = token
			w := f.do(c)
			if w.Code != http.StatusForbidden {
				t.Errorf("sessionOnly=%t %s %s: %d %s", sessionOnly, c.method, c.path, w.Code, w.Body)
			}
		}
		if n := len(f.fake.Requests()); n != 0 {
			t.Errorf("an API key request made %d Stripe calls", n)
		}
	}
}

func TestPlansRoute(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	f.fake.FailNext("/v1/prices", 500, "false")
	w := f.do(call{method: "GET", path: "/api/billing/plans"})
	expect(t, w, http.StatusServiceUnavailable)
	w = f.do(call{method: "GET", path: "/api/billing/plans"})
	expect(t, w, http.StatusOK)
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("Cache-Control = %q", got)
	}
	want := `{"plans":[{"name":"free","features":["basic"],"prices":[]},` +
		`{"name":"pro","features":["basic","export"],"prices":[{"lookup_key":"pro_monthly","currency":"usd","unit_amount":1000,"interval":"month","interval_count":1,"usage_type":"licensed","product_name":"Pro"},` +
		`{"lookup_key":"pro_yearly","currency":"usd","unit_amount":10000,"interval":"year","interval_count":1,"usage_type":"licensed","product_name":"Pro"}]},` +
		`{"name":"team","features":["basic","export","team"],"prices":[{"lookup_key":"team_monthly","currency":"usd","unit_amount":5000,"interval":"month","interval_count":1,"usage_type":"metered","product_name":"Team"}]}]}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Fatalf("body =\n%s\nwant\n%s", got, want)
	}
	if n := strings.Count(f.logs.String(), "pro_gone"); n != 1 {
		t.Fatalf("the missing lookup key was logged %d times, want once", n)
	}
	// The cache serves 5 minutes, with one Stripe call for concurrent misses.
	calls := f.requests("/v1/prices")
	f.do(call{method: "GET", path: "/api/billing/plans"})
	if n := f.requests("/v1/prices"); n != calls {
		t.Fatalf("a cached answer called Stripe %d times", n-calls)
	}
	if n := strings.Count(f.logs.String(), "pro_gone"); n != 1 {
		t.Fatalf("the missing lookup key was logged %d times, want once", n)
	}
	f.clock.Add(5 * time.Minute)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { f.do(call{method: "GET", path: "/api/billing/plans"}) })
	}
	wg.Wait()
	if n := f.requests("/v1/prices"); n != calls+1 {
		t.Fatalf("concurrent misses made %d Stripe calls, want 1", n-calls)
	}
	// Stripe fails after the TTL: the route keeps the old prices.
	f.clock.Add(5 * time.Minute)
	f.fake.FailNext("/v1/prices", 500, "false")
	expect(t, f.do(call{method: "GET", path: "/api/billing/plans"}), http.StatusOK)
}

func TestPlansRouteChunksLookupKeys(t *testing.T) {
	var plans []Plan
	plans = append(plans, Plan{Name: "free"})
	for i := range 3 {
		var prices []string
		for j := range 5 {
			prices = append(prices, "p"+strconv.Itoa(i)+"_"+strconv.Itoa(j))
		}
		plans = append(plans, Plan{Name: "plan" + strconv.Itoa(i), Prices: prices})
	}
	cfg := Config{DefaultPlan: "free", Plans: plans}
	f := newFixture(t, fixtureOptions{cfg: &cfg})
	expect(t, f.do(call{method: "GET", path: "/api/billing/plans"}), http.StatusOK)
	var sizes []int
	for _, r := range f.fake.Requests() {
		if r.Path == "/v1/prices" {
			sizes = append(sizes, len(r.Form["lookup_keys[]"]))
		}
	}
	if len(sizes) != 2 || sizes[0] != 10 || sizes[1] != 5 {
		t.Fatalf("lookup key batches = %v, want [10 5]", sizes)
	}
}

// TestNotConfigured covers a service with no Stripe client (STRIPE_SECRET_KEY
// is not set): the app starts, GET /api/me/billing works from the database,
// and the routes that need Stripe answer 503.
func TestNotConfigured(t *testing.T) {
	f := newFixture(t, fixtureOptions{noClient: true})
	_, cookie := f.register("plain@example.com")
	expect(t, f.do(call{method: "GET", path: SessionPrefix, cookie: cookie}), http.StatusOK)
	for _, c := range []call{
		{method: "POST", path: SessionPrefix + "/checkout", body: `{"price":"pro_monthly"}`, cookie: cookie},
		{method: "POST", path: SessionPrefix + "/portal", body: `{}`, cookie: cookie},
		{method: "POST", path: SessionPrefix + "/sync", body: `{"session_id":"cs_test_1"}`, cookie: cookie},
		{method: "GET", path: "/api/billing/plans"},
	} {
		w := f.do(c)
		expect(t, w, http.StatusServiceUnavailable)
		if !strings.Contains(w.Body.String(), "STRIPE_SECRET_KEY") {
			t.Errorf("%s %s: body = %s", c.method, c.path, w.Body.String())
		}
	}
	if err := f.svc.Sync(t.Context(), "cus_none"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Sync = %v", err)
	}
}

// meteredFixture returns a fixture whose pro plan has a meter and a metered price.
func meteredFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := testConfig()
	cfg.Plans[1].Prices = []string{"pro_monthly", "pro_yearly", "pro_overage"}
	return newFixture(t, fixtureOptions{cfg: &cfg})
}

// checkoutItems returns the line item prices of the last Checkout session request.
func (f *fixture) checkoutItems() (prices []string, quantities []string) {
	var form url.Values
	for _, r := range f.fake.Requests() {
		if r.Path == "/v1/checkout/sessions" {
			form = r.Form
		}
	}
	for i := 0; form.Has("line_items[" + strconv.Itoa(i) + "][price]"); i++ {
		prices = append(prices, form.Get("line_items["+strconv.Itoa(i)+"][price]"))
		quantities = append(quantities, form.Get("line_items["+strconv.Itoa(i)+"][quantity]"))
	}
	return prices, quantities
}

func TestCheckoutRejectsMeteredPriceOfLicensedPlan(t *testing.T) {
	f := meteredFixture(t)
	_, cookie := f.register("metered-alone@example.com")
	w := f.do(call{method: "POST", path: SessionPrefix + "/checkout", body: `{"price":"pro_overage"}`, cookie: cookie})
	expect(t, w, http.StatusBadRequest)
	if !strings.Contains(w.Body.String(), "Try: select a licensed price of the plan") {
		t.Fatalf("body = %s", w.Body)
	}
	if n := f.requests("/v1/checkout/sessions"); n != 0 {
		t.Fatalf("%d Checkout sessions, want 0", n)
	}
}

func TestCheckoutCarriesTheMeteredPrice(t *testing.T) {
	f := meteredFixture(t)
	uid, cookie := f.register("both@example.com")
	w := f.do(call{method: "POST", path: SessionPrefix + "/checkout", body: `{"price":"pro_yearly"}`, cookie: cookie})
	expect(t, w, http.StatusOK)
	prices, quantities := f.checkoutItems()
	if !slices.Equal(prices, []string{"price_pro_y", "price_pro_o"}) || !slices.Equal(quantities, []string{"1", ""}) {
		t.Fatalf("items = %v, quantities = %v", prices, quantities)
	}
	url, _ := decode(t, w)["url"].(string)
	subID, err := f.fake.CompleteCheckout(url[strings.LastIndex(url, "/")+1:])
	if err != nil {
		t.Fatal(err)
	}
	subs, err := f.client.ListSubscriptions(t.Context(), f.customer(uid))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].ID != subID || len(subs[0].Items) != 2 || subs[0].Items[0].Price.LookupKey != "pro_yearly" || subs[0].Items[1].Price.LookupKey != "pro_overage" {
		t.Fatalf("subscriptions = %+v", subs)
	}
	f.drain()
	if e := f.entitlement(uid); e.Plan != "pro" || e.Status != "active" {
		t.Fatalf("entitlement = %+v, want the pro plan", e)
	}
	var lookupKey string
	if err := f.pool.QueryRow(t.Context(), `SELECT price_lookup_key FROM billing_subscriptions WHERE id=$1`, subID).Scan(&lookupKey); err != nil || lookupKey != "pro_yearly" {
		t.Fatalf("stored lookup key = %q, %v: the licensed item decides", lookupKey, err)
	}
	// The usage pass still sends meter events for the plan.
	k, _, err := f.keys.Create(t.Context(), uid, apikeysInput())
	if err != nil {
		t.Fatal(err)
	}
	f.addUsage(k.ID, testStart, 5)
	if err := f.svc.ReportUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.fake.MeterEvents(); len(got) != 1 || got[0].Value != 5 || got[0].EventName != "api_calls" {
		t.Fatalf("meter events = %+v", got)
	}
}

func TestSyncMapsAMeteredItemOrderToThePlan(t *testing.T) {
	f := meteredFixture(t)
	item := func(key, id string, usage string) stripe.SubscriptionItem {
		return stripe.SubscriptionItem{Price: stripe.Price{ID: id, LookupKey: key, Recurring: &stripe.Recurring{UsageType: usage}}}
	}
	// The metered item comes first, and the licensed item still decides.
	row := f.svc.rowOf(stripe.Subscription{ID: "sub_1", Status: "active", Items: []stripe.SubscriptionItem{item("pro_overage", "p1", "metered"), item("pro_monthly", "p2", "licensed")}})
	if row.plan != "pro" || row.lookupKey != "pro_monthly" {
		t.Fatalf("row = %+v", row)
	}
	// A plan of metered prices only maps through its metered item.
	row = f.svc.rowOf(stripe.Subscription{ID: "sub_2", Status: "active", Items: []stripe.SubscriptionItem{item("team_monthly", "p3", "metered")}})
	if row.plan != "team" || row.lookupKey != "team_monthly" {
		t.Fatalf("row = %+v", row)
	}
}
