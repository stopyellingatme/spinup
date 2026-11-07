package stripefake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

// Options configures the catalog, clock, and webhook delivery.
type Options struct {
	// Secret signs webhook events and must not be blank.
	Secret string
	// WebhookURL receives events. An empty URL records events without delivery.
	WebhookURL string
	// Prices is the price catalog.
	Prices []stripe.Price
	// Meters is the billing meter catalog.
	Meters []stripe.Meter
	// Now supplies the clock. The default is time.Now.
	Now func() time.Time
	// Deliver sends webhooks. The default is http.DefaultClient.
	Deliver *http.Client
}

// Request is a recorded API request with parsed form values and headers.
type Request struct {
	// Method is the HTTP method.
	Method string
	// Path is the API path without the query.
	Path string
	// Form contains the query and body parameters.
	Form url.Values
	// Headers contains a copy of the request headers.
	Headers http.Header
}

type checkoutState struct {
	session   stripe.CheckoutSession
	price     stripe.Price
	trialDays int
	metadata  map[string]string
}

type cachedResponse struct {
	path, body string
	data       []byte
}
type failure struct {
	path        string
	status      int
	shouldRetry string
}

// Fake is a concurrent, in-memory Stripe API and webhook source.
type Fake struct {
	mu            sync.Mutex
	o             Options
	server        *httptest.Server
	counters      map[string]int
	customers     map[string]stripe.Customer
	sessions      map[string]*checkoutState
	subscriptions []stripe.Subscription
	idempotency   map[string]cachedResponse
	meterSeen     map[string]time.Time
	meterEvents   []stripe.MeterEvent
	events        []stripe.Event
	requests      []Request
	failures      []failure
}

// New creates a fake without a listener. It panics when Secret is blank.
func New(o Options) *Fake {
	if strings.TrimSpace(o.Secret) == "" {
		panic("create Stripe fake: a webhook secret is required\nTry: supply Options.Secret")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Deliver == nil {
		o.Deliver = http.DefaultClient
	}
	o.Prices = clone(o.Prices)
	o.Meters = clone(o.Meters)
	return &Fake{o: o, counters: map[string]int{}, customers: map[string]stripe.Customer{}, sessions: map[string]*checkoutState{}, idempotency: map[string]cachedResponse{}, meterSeen: map[string]time.Time{}}
}

// Handler returns the Stripe API handler.
func (f *Fake) Handler() http.Handler { return http.HandlerFunc(f.serve) }

type transport struct{ f *Fake }

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	rec := httptest.NewRecorder()
	t.f.Handler().ServeHTTP(rec, r)
	return rec.Result(), nil
}

// Transport returns a transport that serves API requests without sockets.
func (f *Fake) Transport() http.RoundTripper { return transport{f} }

// Start starts a test server. A listener failure returns an error.
func (f *Fake) Start() (address string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("start Stripe fake: %v\nTry: allow a loopback listener or use Transport", recovered)
		}
	}()
	if f.server == nil {
		f.server = httptest.NewServer(f.Handler())
	}
	return f.server.URL, nil
}

// URL returns the server URL, or an empty string before Start.
func (f *Fake) URL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.server == nil {
		return ""
	}
	return f.server.URL
}

// Close stops the test server when it is running.
func (f *Fake) Close() {
	f.mu.Lock()
	server := f.server
	f.server = nil
	f.mu.Unlock()
	if server != nil {
		server.Close()
	}
}

// Events returns a copy of the recorded webhook events.
func (f *Fake) Events() []stripe.Event { f.mu.Lock(); defer f.mu.Unlock(); return clone(f.events) }

// MeterEvents returns a copy of the accepted usage events.
func (f *Fake) MeterEvents() []stripe.MeterEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clone(f.meterEvents)
}

// Requests returns a copy of the recorded API requests.
func (f *Fake) Requests() []Request { f.mu.Lock(); defer f.mu.Unlock(); return clone(f.requests) }

// FailNext injects an error for the next request to the path.
func (f *Fake) FailNext(path string, status int, shouldRetry string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, failure{path, status, shouldRetry})
}

func (f *Fake) id(prefix string) string {
	f.counters[prefix]++
	return fmt.Sprintf("%s%d", prefix, f.counters[prefix])
}

func clone[T any](value T) T {
	data, _ := json.Marshal(value)
	var out T
	_ = json.Unmarshal(data, &out)
	return out
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, _ := json.Marshal(value)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

type apiError struct {
	code, param, message string
	status               int
}

func invalid(code, param, message string) *apiError {
	return &apiError{code: code, param: param, message: message, status: http.StatusBadRequest}
}
func missing(param string) *apiError {
	return invalid("resource_missing", param, "The requested "+param+" does not exist")
}
func writeError(w http.ResponseWriter, e *apiError) {
	kind := "invalid_request_error"
	if e.status == http.StatusUnauthorized {
		kind = "authentication_error"
	}
	if e.status >= 500 {
		kind = "api_error"
	}
	writeJSON(w, e.status, map[string]any{"error": map[string]string{"type": kind, "code": e.code, "param": e.param, "message": e.message}})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Request-Id", f.id("req_"))
	var body []byte
	var err error
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, 1<<20))
	}
	form := r.URL.Query()
	if r.Method == http.MethodPost && err == nil {
		var posted url.Values
		posted, err = url.ParseQuery(string(body))
		for k, v := range posted {
			form[k] = append(form[k], v...)
		}
	}
	f.requests = append(f.requests, Request{Method: r.Method, Path: r.URL.Path, Form: clone(form), Headers: r.Header.Clone()})
	if err != nil {
		writeError(w, invalid("invalid_request", "", "Invalid form body"))
		return
	}
	auth := r.Header.Get("Authorization")
	key, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || (!strings.HasPrefix(key, "sk_test_") && !strings.HasPrefix(key, "rk_test_")) || strings.ContainsAny(key, " \t\r\n") {
		writeError(w, &apiError{status: 401, code: "invalid_api_key", message: "A test secret or restricted key is required"})
		return
	}
	if r.Header.Get("Stripe-Version") != stripe.APIVersion {
		writeError(w, invalid("invalid_request", "Stripe-Version", "The pinned Stripe-Version is required"))
		return
	}
	endpoint := route(r.Method, r.URL.Path)
	if endpoint == "" {
		writeError(w, &apiError{status: 404, code: "resource_missing", message: "Unknown API endpoint"})
		return
	}
	for key := range form {
		if !allowed(endpoint, key) {
			writeError(w, invalid("parameter_unknown", key, "Unknown parameter: "+key))
			return
		}
	}
	for i, fail := range f.failures {
		if fail.path == r.URL.Path {
			f.failures = append(f.failures[:i], f.failures[i+1:]...)
			w.Header().Set("Stripe-Should-Retry", fail.shouldRetry)
			code := "injected_failure"
			if fail.status == 409 {
				code = "lock_timeout"
			}
			writeError(w, &apiError{status: fail.status, code: code, message: "Injected failure"})
			return
		}
	}
	idem := r.Header.Get("Idempotency-Key")
	if r.Method == http.MethodPost && idem != "" {
		if cached, ok := f.idempotency[idem]; ok {
			if cached.path != r.URL.Path || cached.body != string(body) {
				writeError(w, invalid("idempotency_key_in_use", "", "An idempotency key was used with a different request"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(cached.data)
			return
		}
	}
	result, failure := f.dispatch(endpoint, r.URL.Path, form)
	if failure != nil {
		writeError(w, failure)
		return
	}
	data, _ := json.Marshal(result)
	if r.Method == http.MethodPost && idem != "" {
		f.idempotency[idem] = cachedResponse{path: r.URL.Path, body: string(body), data: data}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// CompleteCheckout completes a session and emits the session and subscription events.
func (f *Fake) CompleteCheckout(sessionID string) (string, error) {
	f.mu.Lock()
	state, ok := f.sessions[sessionID]
	if !ok {
		f.mu.Unlock()
		return "", fmt.Errorf("complete Stripe checkout: session does not exist\nTry: create a Checkout session first")
	}
	if state.session.Subscription != "" {
		id := state.session.Subscription
		f.mu.Unlock()
		return id, nil
	}
	now := f.o.Now()
	sub := stripe.Subscription{ID: f.id("sub_"), Customer: state.session.Customer, Status: "active", Items: []stripe.SubscriptionItem{{ID: f.id("si_"), Price: state.price, CurrentPeriodEnd: now.Add(30 * 24 * time.Hour).Unix()}}}
	if state.trialDays > 0 {
		sub.Status = "trialing"
		sub.TrialEnd = now.Add(time.Duration(state.trialDays) * 24 * time.Hour).Unix()
	}
	f.subscriptions = append(f.subscriptions, sub)
	state.session.Status = "complete"
	state.session.PaymentStatus = "paid"
	state.session.Subscription = sub.ID
	if state.trialDays > 0 || state.price.Recurring.UsageType == "metered" {
		state.session.PaymentStatus = "no_payment_required"
	}
	events := []stripe.Event{f.event("checkout.session.completed", sessionObject(state)), f.event("customer.subscription.created", subscriptionObject(sub))}
	f.mu.Unlock()
	var deliveryErr error
	for _, event := range events {
		if err := f.deliver(event); err != nil && deliveryErr == nil {
			deliveryErr = err
		}
	}
	return sub.ID, deliveryErr
}

// SetStatus changes a subscription status and emits the corresponding event.
func (f *Fake) SetStatus(subscriptionID, status string) error {
	switch status {
	case "active", "past_due", "unpaid", "canceled", "paused", "trialing":
	default:
		return fmt.Errorf("change Stripe subscription: invalid status\nTry: use active, past_due, unpaid, canceled, paused, or trialing")
	}
	f.mu.Lock()
	for i := range f.subscriptions {
		sub := &f.subscriptions[i]
		if sub.ID != subscriptionID {
			continue
		}
		sub.Status = status
		kind := "customer.subscription.updated"
		if status == "canceled" {
			kind = "customer.subscription.deleted"
		}
		event := f.event(kind, subscriptionObject(*sub))
		f.mu.Unlock()
		return f.deliver(event)
	}
	f.mu.Unlock()
	return fmt.Errorf("change Stripe subscription: subscription does not exist\nTry: complete a Checkout session first")
}

// Resend delivers an existing event without changing its ID or object.
func (f *Fake) Resend(eventID string) error {
	f.mu.Lock()
	for _, event := range f.events {
		if event.ID == eventID {
			event = clone(event)
			f.mu.Unlock()
			return f.deliver(event)
		}
	}
	f.mu.Unlock()
	return fmt.Errorf("resend Stripe event: event does not exist\nTry: select an ID from Events")
}

func (f *Fake) event(kind string, object any) stripe.Event {
	event := stripe.Event{ID: f.id("evt_"), Type: kind, APIVersion: stripe.APIVersion, Created: f.o.Now().Unix()}
	event.Data.Object, _ = json.Marshal(object)
	f.events = append(f.events, event)
	return event
}

func sessionObject(state *checkoutState) map[string]any {
	data, _ := json.Marshal(state.session)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	object["object"] = "checkout.session"
	object["metadata"] = state.metadata
	return object
}

func subscriptionObject(sub stripe.Subscription) map[string]any {
	data, _ := json.Marshal(sub)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	object["object"] = "subscription"
	object["items"] = map[string]any{"object": "list", "data": sub.Items, "has_more": false}
	return object
}

func (f *Fake) deliver(event stripe.Event) error {
	f.mu.Lock()
	destination := f.o.WebhookURL
	client := f.o.Deliver
	secret := f.o.Secret
	now := f.o.Now()
	f.mu.Unlock()
	if destination == "" {
		return nil
	}
	body, _ := json.Marshal(event)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, destination, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("deliver Stripe webhook: invalid webhook URL\nTry: correct Options.WebhookURL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", stripe.SignatureHeader(body, secret, now))
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("deliver Stripe webhook: delivery failed\nTry: check the webhook handler and use Resend")
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("deliver Stripe webhook: status %d\nTry: correct the webhook handler and use Resend", res.StatusCode)
	}
	return nil
}
