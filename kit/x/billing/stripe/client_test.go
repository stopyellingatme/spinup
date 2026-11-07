package stripe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func answer(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func testClient(t *testing.T, transport roundTripFunc) *Client {
	t.Helper()
	c, err := New("sk_test_private", WithBaseURL("http://stripe.fake"), WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	c.o.wait = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestCustomerFormAndHeaders(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != "email=a%2Bb%40example.com&metadata%5Ba%5D=1&metadata%5Bz%5D=2&name=A+Name" {
			t.Errorf("body = %s", body)
		}
		for key, want := range map[string]string{"Authorization": "Bearer sk_test_private", "Stripe-Version": APIVersion, "User-Agent": "spinup-kit-billing", "Content-Type": "application/x-www-form-urlencoded", "Idempotency-Key": "customer:7"} {
			if got := r.Header.Get(key); got != want {
				t.Errorf("%s = %q", key, got)
			}
		}
		return answer(200, `{"id":"cus_1","email":"a+b@example.com","metadata":{"a":"1"}}`), nil
	})
	got, err := c.CreateCustomer(t.Context(), CustomerParams{Email: "a+b@example.com", Name: "A Name", Metadata: map[string]string{"z": "2", "a": "1"}}, "customer:7")
	if err != nil || got.ID != "cus_1" || got.Metadata["a"] != "1" {
		t.Fatalf("got %+v, %v", got, err)
	}
	for _, key := range []string{"", " \t\n"} {
		if _, err := New(key); err == nil {
			t.Fatal("accepted blank key")
		}
	}
}

func TestCheckoutForms(t *testing.T) {
	for _, metered := range []bool{false, true} {
		c := testClient(t, func(r *http.Request) (*http.Response, error) {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			want := url.Values{"mode": {"subscription"}, "ui_mode": {"hosted_page"}, "customer": {"cus_1"}, "line_items[0][price]": {"price_1"}, "client_reference_id": {"7"}, "success_url": {"https://app/success"}, "cancel_url": {"https://app/cancel"}, "subscription_data[trial_period_days]": {"5"}, "automatic_tax[enabled]": {"true"}, "allow_promotion_codes": {"true"}, "metadata[user_id]": {"7"}}
			if !metered {
				want.Set("line_items[0][quantity]", "1")
			}
			if r.PostForm.Encode() != want.Encode() {
				t.Errorf("form = %s, want %s", r.PostForm.Encode(), want.Encode())
			}
			return answer(200, `{"id":"cs_test_1","url":"https://checkout.stripe.fake"}`), nil
		})
		_, err := c.CreateCheckoutSession(t.Context(), CheckoutParams{Customer: "cus_1", Price: "price_1", ClientReferenceID: "7", SuccessURL: "https://app/success", CancelURL: "https://app/cancel", TrialDays: 5, AutomaticTax: true, AllowPromotionCodes: true, Metered: metered, Metadata: map[string]string{"user_id": "7"}}, "")
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckoutMeteredPrices(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		if r.PostForm.Get("line_items[0][price]") != "price_1" || r.PostForm.Get("line_items[0][quantity]") != "1" ||
			r.PostForm.Get("line_items[1][price]") != "price_m1" || r.PostForm.Get("line_items[2][price]") != "price_m2" ||
			r.PostForm.Has("line_items[1][quantity]") || r.PostForm.Has("line_items[2][quantity]") {
			t.Errorf("form = %s", r.PostForm.Encode())
		}
		return answer(200, `{"id":"cs_test_1","url":"https://checkout.stripe.fake"}`), nil
	})
	_, err := c.CreateCheckoutSession(t.Context(), CheckoutParams{Customer: "cus_1", Price: "price_1", MeteredPrices: []string{"price_m1", "price_m2"}, ClientReferenceID: "7", SuccessURL: "https://app/s", CancelURL: "https://app/c"}, "")
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetries(t *testing.T) {
	cases := []struct {
		name           string
		status         int
		code, override string
		network        bool
		tries          int
	}{
		{"network", 0, "", "", true, 3}, {"lock", 409, "lock_timeout", "", false, 3},
		{"other conflict", 409, "other", "", false, 1}, {"rate limit", 429, "", "", false, 3},
		{"server", 500, "", "", false, 3}, {"outside 5xx", 600, "", "", false, 1}, {"force", 400, "", "true", false, 3},
		{"suppress", 500, "", "false", false, 1}, {"bad request", 400, "", "", false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tries := 0
			var key, body string
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				tries++
				b, _ := io.ReadAll(r.Body)
				if tries == 1 {
					key = r.Header.Get("Idempotency-Key")
					body = string(b)
				}
				if key == "" || r.Header.Get("Idempotency-Key") != key || string(b) != body {
					t.Error("retry changed key or body")
				}
				if tc.network {
					return nil, errors.New("connection lost")
				}
				res := answer(tc.status, `{"error":{"code":"`+tc.code+`","message":"failure"}}`)
				res.Header.Set("Stripe-Should-Retry", tc.override)
				return res, nil
			})
			_, err := c.CreateCustomer(t.Context(), CustomerParams{}, "")
			if err == nil || tries != tc.tries {
				t.Fatalf("tries = %d, error = %v", tries, err)
			}
		})
	}
}

func TestRetrySuccessKeysAndCancellation(t *testing.T) {
	keys := []string{}
	tries := 0
	waits := []time.Duration{}
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		tries++
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if tries < 3 {
			return answer(503, `{}`), nil
		}
		return answer(200, `{"id":"cus_1"}`), nil
	})
	c.o.wait = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	if _, err := c.CreateCustomer(t.Context(), CustomerParams{}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateCustomer(t.Context(), CustomerParams{}, ""); err != nil {
		t.Fatal(err)
	}
	if keys[0] != keys[1] || keys[1] != keys[2] || keys[2] == keys[3] {
		t.Fatal(keys)
	}
	if len(waits) != 2 || waits[0] < 375*time.Millisecond || waits[0] > 625*time.Millisecond || waits[1] < 750*time.Millisecond || waits[1] > 1250*time.Millisecond {
		t.Fatal(waits)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	c.o.wait = wait
	if _, err := c.CreateCustomer(ctx, CustomerParams{}, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPaginationAndWireShapes(t *testing.T) {
	pages := 0
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		pages++
		q := r.URL.Query()
		if q.Get("limit") != "100" || q.Get("customer") != "cus_1" || q.Get("status") != "all" {
			t.Error(q)
		}
		if pages == 1 {
			return answer(200, `{"data":[{"id":"sub_1","customer":"cus_1","items":{"data":[{"id":"si_1","current_period_end":123,"price":{"id":"price_1","product":"prod_1","recurring":{"meter":"mtr_1"}}}]}}],"has_more":true}`), nil
		}
		if q.Get("starting_after") != "sub_1" {
			t.Error(q)
		}
		return answer(200, `{"data":[{"id":"sub_2","items":{"data":[]}}],"has_more":false}`), nil
	})
	subs, err := c.ListSubscriptions(t.Context(), "cus_1")
	if err != nil || len(subs) != 2 {
		t.Fatalf("%+v, %v", subs, err)
	}
	item := subs[0].Items[0]
	if item.CurrentPeriodEnd != 123 || item.Price.Product.ID != "prod_1" || item.Price.Recurring.Meter != "mtr_1" {
		t.Fatal(item)
	}
	// More than the 10 embedded items: the client reads the item list.
	c = testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/subscription_items" {
			if r.URL.Query().Get("subscription") != "sub_9" {
				t.Error(r.URL.Query())
			}
			return answer(200, `{"data":[{"id":"si_1","price":{"id":"price_a","lookup_key":"a"}},{"id":"si_11","price":{"id":"price_pro","lookup_key":"pro_monthly"}}],"has_more":false}`), nil
		}
		return answer(200, `{"data":[{"id":"sub_9","items":{"data":[{"id":"si_1","price":{"id":"price_a","lookup_key":"a"}}],"has_more":true}}],"has_more":false}`), nil
	})
	subs, err = c.ListSubscriptions(t.Context(), "cus_1")
	if err != nil || len(subs) != 1 || len(subs[0].Items) != 2 || subs[0].Items[1].Price.LookupKey != "pro_monthly" {
		t.Fatalf("item pages = %+v, %v", subs, err)
	}
	pages = 0
	c = testClient(t, func(r *http.Request) (*http.Response, error) {
		pages++
		return answer(200, `{"data":[{"id":"price_1","product":{"id":"prod_1","name":"Pro"}}],"has_more":true}`), nil
	})
	if _, err := c.ListPrices(t.Context(), []string{"pro"}); err == nil || pages != 10 {
		t.Fatalf("pages %d, error %v", pages, err)
	}
	c = testClient(t, func(r *http.Request) (*http.Response, error) { return answer(200, `{"data":[],"has_more":true}`), nil })
	if _, err := c.ListMeters(t.Context()); err == nil {
		t.Fatal("accepted empty continuation")
	}
}

func TestOtherEndpoints(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/prices":
			q := r.URL.Query()
			if q.Get("active") != "true" || q.Get("expand[]") != "data.product" || strings.Join(q["lookup_keys[]"], ",") != "a,b" {
				t.Error(q)
			}
			return answer(200, `{"data":[{"id":"price_1","product":{"id":"prod_1","name":"Pro"}}]}`), nil
		case "/v1/billing/meters":
			return answer(200, `{"data":[{"id":"mtr_1","event_name":"calls","status":"active"}]}`), nil
		case "/v1/checkout/sessions/cs_test_1":
			return answer(200, `{"id":"cs_test_1","subscription":"sub_1"}`), nil
		case "/v1/billing_portal/sessions":
			_ = r.ParseForm()
			if r.PostForm.Get("customer") != "cus_1" || r.PostForm.Get("return_url") != "https://app/return" || r.Header.Get("Idempotency-Key") == "" {
				t.Error(r.PostForm)
			}
			return answer(200, `{"id":"bpc_1","url":"https://billing.stripe.fake"}`), nil
		case "/v1/billing/meter_events":
			_ = r.ParseForm()
			want := url.Values{"event_name": {"calls"}, "payload[stripe_customer_id]": {"cus_1"}, "payload[value]": {"9"}, "identifier": {"usage:1"}, "timestamp": {"1800000000"}}
			if r.PostForm.Encode() != want.Encode() {
				t.Error(r.PostForm)
			}
			return answer(200, `{}`), nil
		case "/v1/balance":
			return answer(200, `{}`), nil
		default:
			t.Fatalf("unexpected %s", r.URL)
			return nil, nil
		}
	})
	prices, err := c.ListPrices(t.Context(), []string{"a", "b"})
	if err != nil || prices[0].Product.Name != "Pro" {
		t.Fatalf("%+v %v", prices, err)
	}
	meters, err := c.ListMeters(t.Context())
	if err != nil || meters[0].EventName != "calls" {
		t.Fatalf("%+v %v", meters, err)
	}
	session, err := c.GetCheckoutSession(t.Context(), "cs_test_1")
	if err != nil || session.Subscription != "sub_1" {
		t.Fatalf("%+v %v", session, err)
	}
	portal, err := c.CreatePortalSession(t.Context(), "cus_1", "https://app/return")
	if err != nil || portal.ID != "bpc_1" {
		t.Fatalf("%+v %v", portal, err)
	}
	if err := c.CreateMeterEvent(t.Context(), MeterEvent{EventName: "calls", Customer: "cus_1", Identifier: "usage:1", Value: 9, Timestamp: time.Unix(1800000000, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckKey(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestErrorNeverContainsKey(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		res := answer(401, `{"error":{"type":"authentication_error","code":"invalid_key","param":"key","message":"Invalid key: sk_test_private"}}`)
		res.Header.Set("Request-Id", "req_1")
		return res, nil
	})
	err := c.CheckKey(t.Context())
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.Type != "authentication_error" || apiErr.Code != "invalid_key" || apiErr.Param != "key" || apiErr.RequestID != "req_1" {
		t.Fatalf("%#v", err)
	}
	for _, part := range []string{"GET", "/v1/balance", "401", "Invalid key:", "req_1", "\nTry:"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("missing %q: %v", part, err)
		}
	}
	if strings.Contains(err.Error(), "sk_test_private") {
		t.Fatal("secret leaked")
	}
}

func TestClientConfiguration(t *testing.T) {
	c, err := New("sk_test_key")
	if err != nil || c.o.http.Timeout != 20*time.Second || c.o.baseURL != DefaultBaseURL || c.o.retries != 2 {
		t.Fatalf("%+v, %v", c, err)
	}
	for _, option := range []Option{WithBaseURL("invalid"), WithBaseURL("ftp://stripe.fake"), WithBaseURL("http://user:pass@stripe.fake"), WithBaseURL("http://stripe.fake?key=value"), WithHTTPClient(nil), WithRetries(-1)} {
		if _, err := New("sk_test_key", option); err == nil {
			t.Fatal("accepted invalid option")
		}
	}
}

func TestInvalidResponsesAndTransportRedaction(t *testing.T) {
	for _, body := range []string{`{`, `{"data":[{"id":"price_1","product":42}]}`} {
		c := testClient(t, func(r *http.Request) (*http.Response, error) { return answer(200, body), nil })
		if _, err := c.ListPrices(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("accepted %s: %v", body, err)
		}
	}
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("transport rejected sk_test_private")
	})
	if err := c.CheckKey(t.Context()); err == nil || strings.Contains(err.Error(), "sk_test_private") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestCancelDuringRetryWait(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempts := 0
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		attempts++
		return answer(http.StatusServiceUnavailable, `{}`), nil
	})
	c.o.wait = func(ctx context.Context, d time.Duration) error { cancel(); return wait(ctx, d) }
	if err := c.CheckKey(ctx); !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("attempts %d, error %v", attempts, err)
	}
}

func TestCancellationDuringHTTPRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := testClient(t, func(r *http.Request) (*http.Response, error) { cancel(); return nil, ctx.Err() })
	c.o.retries = 0
	if err := c.CheckKey(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}
