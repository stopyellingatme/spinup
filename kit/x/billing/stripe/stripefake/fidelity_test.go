package stripefake

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

func fixture(t *testing.T) (*Fake, *stripe.Client, time.Time) {
	t.Helper()
	now := time.Unix(1800000000, 0)
	f := New(Options{Secret: "whsec_test", Now: func() time.Time { return now }, Prices: []stripe.Price{
		{ID: "price_licensed", LookupKey: "pro", Active: true, Recurring: &stripe.Recurring{Interval: "month", IntervalCount: 1, UsageType: "licensed"}, Product: stripe.Product{ID: "prod_1", Name: "Pro"}},
		{ID: "price_metered", LookupKey: "usage", Active: true, Recurring: &stripe.Recurring{Interval: "month", UsageType: "metered", Meter: "mtr_1"}},
		{ID: "price_inactive", LookupKey: "old", Active: false, Recurring: &stripe.Recurring{UsageType: "licensed"}},
		{ID: "price_once", LookupKey: "once", Active: true},
	}, Meters: []stripe.Meter{{ID: "mtr_1", EventName: "calls", Status: "active"}}})
	c, err := stripe.New("sk_test_key", stripe.WithBaseURL("http://stripe.fake"), stripe.WithHTTPClient(&http.Client{Transport: f.Transport()}), stripe.WithRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	return f, c, now
}

func raw(t *testing.T, f *Fake, method, path string, form url.Values, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(form.Encode())
	} else if len(form) > 0 {
		path += "?" + form.Encode()
	}
	req := httptest.NewRequestWithContext(t.Context(), method, "http://stripe.fake"+path, body)
	req.Header.Set("Authorization", "Bearer sk_test_key")
	req.Header.Set("Stripe-Version", stripe.APIVersion)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Idempotency-Key", "raw:"+path+":"+form.Encode())
	}
	for k, v := range headers {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	f.Handler().ServeHTTP(rec, req)
	return rec
}

func expectCode(t *testing.T, res *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	b := res.Body.Bytes()
	if res.Code != status {
		t.Fatalf("status = %d, want %d: %s", res.Code, status, b)
	}
	if code != "" {
		var got struct{ Error struct{ Code string } }
		if json.Unmarshal(b, &got) != nil || got.Error.Code != code {
			t.Fatalf("want code %q, body %s", code, b)
		}
	}
}

func customer(t *testing.T, c *stripe.Client) string {
	t.Helper()
	got, err := c.CreateCustomer(t.Context(), stripe.CustomerParams{Email: "a@example.com"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return got.ID
}
func checkout(t *testing.T, c *stripe.Client, customerID string, trial int) *stripe.CheckoutSession {
	t.Helper()
	s, err := c.CreateCheckoutSession(t.Context(), stripe.CheckoutParams{Customer: customerID, Price: "price_licensed", SuccessURL: "https://app/success", CancelURL: "https://app/cancel", ClientReferenceID: "7", TrialDays: trial, Metadata: map[string]string{"user_id": "7"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFidelityAuthorization(t *testing.T) {
	f, _, _ := fixture(t)
	for _, key := range []string{"", "Bearer sk_live_key", "Basic sk_test_key", "Bearer pk_test_key"} {
		expectCode(t, raw(t, f, "GET", "/v1/balance", nil, http.Header{"Authorization": {key}}), 401, "")
	}
	for _, key := range []string{"Bearer sk_test_key", "Bearer rk_test_key"} {
		expectCode(t, raw(t, f, "GET", "/v1/balance", nil, http.Header{"Authorization": {key}}), 200, "")
	}
}

func TestFidelityUnknownParameters(t *testing.T) {
	f, _, _ := fixture(t)
	for _, endpoint := range []struct{ method, path string }{
		{"GET", "/v1/balance"}, {"POST", "/v1/customers"}, {"POST", "/v1/checkout/sessions"},
		{"GET", "/v1/checkout/sessions/cs_missing"}, {"POST", "/v1/billing_portal/sessions"},
		{"GET", "/v1/subscriptions"}, {"GET", "/v1/prices"}, {"GET", "/v1/billing/meters"}, {"POST", "/v1/billing/meter_events"},
	} {
		t.Run(endpoint.path, func(t *testing.T) {
			expectCode(t, raw(t, f, endpoint.method, endpoint.path, url.Values{"unknown": {"x"}}, nil), 400, "parameter_unknown")
		})
	}
}

func TestFidelityAPIVersion(t *testing.T) {
	f, _, _ := fixture(t)
	for _, version := range []string{"", "2025-03-31.basil"} {
		expectCode(t, raw(t, f, "GET", "/v1/balance", nil, http.Header{"Stripe-Version": {version}}), 400, "")
	}
}

func TestFidelityIdempotency(t *testing.T) {
	f, c, _ := fixture(t)
	one, err := c.CreateCustomer(t.Context(), stripe.CustomerParams{Email: "a@example.com"}, "fixed")
	if err != nil {
		t.Fatal(err)
	}
	two, err := c.CreateCustomer(t.Context(), stripe.CustomerParams{Email: "a@example.com"}, "fixed")
	if err != nil || one.ID != two.ID {
		t.Fatalf("%+v %v", two, err)
	}
	_, err = c.CreateCustomer(t.Context(), stripe.CustomerParams{Email: "b@example.com"}, "fixed")
	if err == nil || !strings.Contains(err.Error(), "different") {
		t.Fatalf("changed body: %v", err)
	}
	reqs := f.Requests()
	if len(reqs) != 3 || reqs[0].Headers.Get("Idempotency-Key") != "fixed" {
		t.Fatal(reqs)
	}
	// A cached session remains the original response after completion.
	session := checkout(t, c, one.ID, 0)
	_, err = f.CompleteCheckout(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := f.Requests()[3]
	res := raw(t, f, r.Method, r.Path, r.Form, r.Headers)
	var replay stripe.CheckoutSession
	if err := json.NewDecoder(res.Body).Decode(&replay); err != nil || replay.Status != "open" {
		t.Fatalf("replay %+v, %v", replay, err)
	}
}

func TestFidelityMissingResources(t *testing.T) {
	f, c, _ := fixture(t)
	id := customer(t, c)
	forms := []struct {
		path string
		form url.Values
	}{
		{"/v1/billing_portal/sessions", url.Values{"customer": {"cus_missing"}, "return_url": {"https://app"}}},
		{"/v1/checkout/sessions", url.Values{"mode": {"subscription"}, "ui_mode": {"hosted_page"}, "customer": {"cus_missing"}, "line_items[0][price]": {"price_licensed"}, "line_items[0][quantity]": {"1"}, "success_url": {"https://app"}, "cancel_url": {"https://app"}}},
		{"/v1/checkout/sessions", url.Values{"mode": {"subscription"}, "ui_mode": {"hosted_page"}, "customer": {id}, "line_items[0][price]": {"price_missing"}, "line_items[0][quantity]": {"1"}, "success_url": {"https://app"}, "cancel_url": {"https://app"}}},
	}
	for _, tc := range forms {
		expectCode(t, raw(t, f, "POST", tc.path, tc.form, nil), 400, "resource_missing")
	}
	expectCode(t, raw(t, f, "GET", "/v1/checkout/sessions/cs_missing", nil, nil), 400, "resource_missing")
	expectCode(t, raw(t, f, "GET", "/v1/subscriptions", url.Values{"customer": {"cus_missing"}, "status": {"all"}}, nil), 400, "resource_missing")
	if _, err := f.CompleteCheckout("cs_missing"); err == nil {
		t.Fatal("accepted missing session")
	}
	if err := f.SetStatus("sub_missing", "active"); err == nil {
		t.Fatal("accepted missing subscription")
	}
}

func TestFidelityCheckout(t *testing.T) {
	f, c, _ := fixture(t)
	id := customer(t, c)
	base := url.Values{"mode": {"subscription"}, "ui_mode": {"hosted_page"}, "customer": {id}, "line_items[0][price]": {"price_licensed"}, "line_items[0][quantity]": {"1"}, "success_url": {"https://app"}, "cancel_url": {"https://app"}}
	for _, tc := range []struct{ key, value string }{
		{"mode", "payment"}, {"ui_mode", "hosted"}, {"line_items[0][price]", "price_inactive"},
		{"line_items[0][price]", "price_once"}, {"line_items[0][quantity]", "2"}, {"line_items[0][quantity]", ""},
		{"line_items[0][price]", "price_metered"},
	} {
		form := clone(base)
		form.Set(tc.key, tc.value)
		expectCode(t, raw(t, f, "POST", "/v1/checkout/sessions", form, nil), 400, "")
	}
	delete(base, "line_items[0][quantity]")
	base.Set("line_items[0][price]", "price_metered")
	expectCode(t, raw(t, f, "POST", "/v1/checkout/sessions", base, nil), 200, "")
	session := checkout(t, c, id, 0)
	if session.ID != "cs_test_2" || session.URL != "https://checkout.stripe.fake/c/pay/"+session.ID {
		t.Fatal(session)
	}
	portal, err := c.CreatePortalSession(t.Context(), id, "https://app/return")
	if err != nil || portal.URL != "https://billing.stripe.fake/p/session/"+portal.ID || portal.ID != "bpc_1" {
		t.Fatalf("%+v %v", portal, err)
	}
}

func TestFidelityMeterEvents(t *testing.T) {
	f, c, now := fixture(t)
	id := customer(t, c)
	e := stripe.MeterEvent{Customer: id, EventName: "calls", Identifier: "usage:1", Value: 3, Timestamp: now}
	if err := c.CreateMeterEvent(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if err := c.CreateMeterEvent(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if len(f.MeterEvents()) != 1 {
		t.Fatal(f.MeterEvents())
	}
	for _, stamp := range []time.Time{now.Add(-35*24*time.Hour - time.Second), now.Add(5*time.Minute + time.Second)} {
		e.Timestamp = stamp
		e.Identifier = stamp.String()
		if err := c.CreateMeterEvent(t.Context(), e); err == nil {
			t.Fatalf("accepted %s", stamp)
		}
	}
	for _, stamp := range []time.Time{now.Add(-35 * 24 * time.Hour), now.Add(5 * time.Minute)} {
		e.Timestamp = stamp
		e.Identifier = stamp.String()
		if err := c.CreateMeterEvent(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	e.Customer = "cus_missing"
	if err := c.CreateMeterEvent(t.Context(), e); err == nil {
		t.Fatal("accepted missing customer")
	}
}

func TestFidelityMeterDedupeExpires(t *testing.T) {
	now := time.Unix(1800000000, 0)
	f := New(Options{Secret: "secret", Now: func() time.Time { return now }, Meters: []stripe.Meter{{ID: "mtr_1", EventName: "calls", Status: "active"}}})
	c, err := stripe.New("sk_test_key", stripe.WithBaseURL("http://stripe.fake"), stripe.WithHTTPClient(&http.Client{Transport: f.Transport()}))
	if err != nil {
		t.Fatal(err)
	}
	id := customer(t, c)
	e := stripe.MeterEvent{Customer: id, EventName: "calls", Identifier: "same", Value: 1, Timestamp: now}
	if err := c.CreateMeterEvent(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24*time.Hour + time.Second)
	e.Timestamp = now
	if err := c.CreateMeterEvent(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if len(f.MeterEvents()) != 2 {
		t.Fatal(f.MeterEvents())
	}
}

func TestFidelitySubscriptionsIncludeCanceled(t *testing.T) {
	f, c, now := fixture(t)
	id := customer(t, c)
	session := checkout(t, c, id, 3)
	subID, err := f.CompleteCheckout(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	subs, err := c.ListSubscriptions(t.Context(), id)
	if err != nil || len(subs) != 1 {
		t.Fatalf("%+v %v", subs, err)
	}
	sub := subs[0]
	if sub.Status != "trialing" || sub.TrialEnd != now.Add(72*time.Hour).Unix() || len(sub.Items) != 1 || sub.Items[0].CurrentPeriodEnd != now.Add(30*24*time.Hour).Unix() {
		t.Fatal(sub)
	}
	if err := f.SetStatus(subID, "canceled"); err != nil {
		t.Fatal(err)
	}
	subs, err = c.ListSubscriptions(t.Context(), id)
	if err != nil || len(subs) != 1 || subs[0].Status != "canceled" {
		t.Fatalf("%+v %v", subs, err)
	}
	if err := f.SetStatus(subID, "made_up"); err == nil {
		t.Fatal("accepted invalid status")
	}
}

type deliverFunc func(*http.Request) (*http.Response, error)

func (fn deliverFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestSignedLifecycleAndResend(t *testing.T) {
	f, c, now := fixture(t)
	var delivered []stripe.Event
	f.o.WebhookURL = "http://app.fake/api/billing/webhook"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		event, err := stripe.VerifyWebhook(b, r.Header.Get("Stripe-Signature"), []string{"whsec_test"}, now)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		delivered = append(delivered, event)
		w.WriteHeader(http.StatusOK)
	})
	f.o.Deliver = &http.Client{Transport: deliverFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Result(), nil
	})}
	id := customer(t, c)
	session := checkout(t, c, id, 0)
	subID, err := f.CompleteCheckout(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"past_due", "unpaid", "paused", "trialing", "active", "canceled"} {
		if err := f.SetStatus(subID, status); err != nil {
			t.Fatal(err)
		}
	}
	events := f.Events()
	if len(events) != 8 || len(delivered) != 8 || events[0].Type != "checkout.session.completed" || events[1].Type != "customer.subscription.created" || events[7].Type != "customer.subscription.deleted" {
		t.Fatal(events)
	}
	for _, event := range events {
		if event.APIVersion != stripe.APIVersion || event.Customer() != id || event.Created != now.Unix() {
			t.Fatal(event)
		}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(events[0].Data.Object, &object); err != nil || string(object["status"]) != `"complete"` || string(object["client_reference_id"]) != `"7"` {
		t.Fatalf("%s, %v", events[0].Data.Object, err)
	}
	if err := json.Unmarshal(events[1].Data.Object, &object); err != nil || !bytes.Contains(object["items"], []byte(`"current_period_end"`)) {
		t.Fatalf("%s, %v", events[1].Data.Object, err)
	}
	if err := f.Resend(events[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(f.Events()) != 8 || len(delivered) != 9 || delivered[8].ID != events[0].ID || !bytes.Equal(delivered[8].Data.Object, events[0].Data.Object) {
		t.Fatal("resend changed event")
	}
	if err := f.Resend("evt_missing"); err == nil {
		t.Fatal("accepted missing event")
	}
	if _, err := f.CompleteCheckout(session.ID); err != nil || len(f.Events()) != 8 {
		t.Fatal("duplicate completion")
	}
}

func TestFakeSnapshotsFailuresAndConcurrency(t *testing.T) {
	f, c, _ := fixture(t)
	f.FailNext("/v1/balance", 429, "false")
	expectErr := c.CheckKey(t.Context())
	if expectErr == nil {
		t.Fatal("missing injected error")
	}
	if err := c.CheckKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			if _, err := c.CreateCustomer(t.Context(), stripe.CustomerParams{}, ""); err != nil {
				t.Error(err)
			}
			_ = f.Requests()
			_ = f.Events()
			_ = f.MeterEvents()
			_ = f.URL()
		})
	}
	wg.Wait()
	reqs := f.Requests()
	reqs[0].Headers.Set("Stripe-Version", "changed")
	reqs[0].Form.Set("changed", "yes")
	if f.Requests()[0].Headers.Get("Stripe-Version") != stripe.APIVersion || f.Requests()[0].Form.Get("changed") != "" {
		t.Fatal("mutable request snapshot")
	}
	prices, err := c.ListPrices(t.Context(), []string{"pro", "old"})
	if err != nil || len(prices) != 1 || prices[0].Product.Name != "Pro" {
		t.Fatalf("%+v %v", prices, err)
	}
}

func TestStart(t *testing.T) {
	f, _, _ := fixture(t)
	if f.URL() != "" {
		t.Fatal(f.URL())
	}
	address, err := f.Start()
	if err != nil {
		t.Skipf("socket unavailable: %v", err)
	}
	defer f.Close()
	if address == "" || address != f.URL() {
		t.Fatal(address)
	}
}

func TestPagination(t *testing.T) {
	f, c, _ := fixture(t)
	id := customer(t, c)
	for i := 0; i < 102; i++ {
		s := checkout(t, c, id, 0)
		if _, err := f.CompleteCheckout(s.ID); err != nil {
			t.Fatal(err)
		}
	}
	subs, err := c.ListSubscriptions(t.Context(), id)
	if err != nil || len(subs) != 102 {
		t.Fatalf("%d: %v", len(subs), err)
	}
	reqs := f.Requests()
	last := reqs[len(reqs)-1]
	if last.Form.Get("starting_after") == "" || last.Form.Get("limit") != "100" {
		t.Fatal(last)
	}
	for i := 0; i < 102; i++ {
		f.o.Prices = append(f.o.Prices, stripe.Price{ID: fmt.Sprintf("price_%d", i), Active: true})
	}
	prices, err := c.ListPrices(t.Context(), nil)
	if err != nil || len(prices) != 105 {
		t.Fatalf("%d: %v", len(prices), err)
	}
}

func TestWebhookDeliveryFailureAndResend(t *testing.T) {
	f, c, _ := fixture(t)
	attempts := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	f.o.WebhookURL = "http://app.fake/webhook"
	f.o.Deliver = &http.Client{Transport: deliverFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Result(), nil
	})}
	id := customer(t, c)
	session := checkout(t, c, id, 0)
	subID, err := f.CompleteCheckout(session.ID)
	if err == nil || subID == "" || attempts != 2 || len(f.Events()) != 2 {
		t.Fatalf("subscription %s, attempts %d, error %v", subID, attempts, err)
	}
	if err := f.Resend(f.Events()[0].ID); err != nil || attempts != 3 {
		t.Fatalf("attempts %d, error %v", attempts, err)
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestTransportClosesRequestBody(t *testing.T) {
	f, _, _ := fixture(t)
	body := &trackedBody{Reader: strings.NewReader("email=a%40example.com")}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://stripe.fake/v1/customers", body)
	req.Header.Set("Authorization", "Bearer sk_test_key")
	req.Header.Set("Stripe-Version", stripe.APIVersion)
	res, err := f.Transport().RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if !body.closed {
		t.Fatal("transport did not close the request body")
	}
}

func TestFailNextRetryAndIdempotency(t *testing.T) {
	f, _, _ := fixture(t)
	c, err := stripe.New("rk_test_key", stripe.WithBaseURL("http://stripe.fake"), stripe.WithHTTPClient(&http.Client{Transport: f.Transport()}), stripe.WithRetries(1))
	if err != nil {
		t.Fatal(err)
	}
	f.FailNext("/v1/customers", http.StatusBadRequest, "true")
	if _, err := c.CreateCustomer(t.Context(), stripe.CustomerParams{}, ""); err != nil {
		t.Fatal(err)
	}
	reqs := f.Requests()
	if len(reqs) != 2 || reqs[0].Headers.Get("Idempotency-Key") == "" || reqs[0].Headers.Get("Idempotency-Key") != reqs[1].Headers.Get("Idempotency-Key") {
		t.Fatal(reqs)
	}
}

func TestNewRequiresSecret(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("accepted blank secret")
		}
	}()
	New(Options{})
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, fmt.Errorf("read failed") }

func TestHandlerRejectsBodyReadFailure(t *testing.T) {
	f, _, _ := fixture(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://stripe.fake/v1/customers", brokenReader{})
	req.Header.Set("Authorization", "Bearer sk_test_key")
	req.Header.Set("Stripe-Version", stripe.APIVersion)
	rec := httptest.NewRecorder()
	f.Handler().ServeHTTP(rec, req)
	expectCode(t, rec, http.StatusBadRequest, "invalid_request")
}
