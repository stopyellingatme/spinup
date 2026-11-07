package billing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/x/apikeys"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

func eventBody(t *testing.T, id, kind, customer string) []byte {
	t.Helper()
	object, _ := json.Marshal(map[string]any{"id": "sub_x", "object": "subscription", "customer": customer})
	body, err := json.Marshal(map[string]any{"id": id, "type": kind, "api_version": stripe.APIVersion, "created": testStart.Unix(), "data": map[string]any{"object": json.RawMessage(object)}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (f *fixture) post(body []byte, signature string) *httptest.ResponseRecorder {
	f.t.Helper()
	h := http.Header{}
	if signature != "" {
		h.Set("Stripe-Signature", signature)
	}
	return f.do(call{method: "POST", path: WebhookPath, body: string(body), header: h})
}

func TestWebhookSignature(t *testing.T) {
	f := newFixture(t, fixtureOptions{opts: []Option{WithWebhookSecrets("whsec_old, " + testSecret)}})
	body := eventBody(t, "evt_sig", "customer.subscription.updated", "cus_x")
	good := stripe.SignatureHeader(body, testSecret, testStart)
	cases := map[string]string{
		"missing header":  "",
		"wrong secret":    stripe.SignatureHeader(body, "whsec_wrong", testStart),
		"too old":         stripe.SignatureHeader(body, testSecret, testStart.Add(-6*time.Minute)),
		"in the future":   stripe.SignatureHeader(body, testSecret, testStart.Add(6*time.Minute)),
		"no v1":           "t=" + strconv.FormatInt(testStart.Unix(), 10),
		"bad timestamp":   "t=abc,v1=00",
		"tampered digest": good[:len(good)-2] + "00",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			w := f.post(body, header)
			expect(t, w, http.StatusBadRequest)
			for _, leak := range []string{"tolerance", "match", "timestamp", "v1"} {
				if strings.Contains(w.Body.String(), leak) {
					t.Errorf("the answer names the failed check (%q): %s", leak, w.Body)
				}
			}
			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q", got)
			}
		})
	}
	if n := f.count(`SELECT count(*) FROM billing_events`); n != 0 {
		t.Fatalf("a rejected event wrote %d rows", n)
	}
	if !strings.Contains(f.logs.String(), "level=WARN") || !strings.Contains(f.logs.String(), "rejected a webhook") {
		t.Errorf("no warning was logged: %s", f.logs)
	}
	// The second secret of the rotation list verifies the event.
	w := f.post(body, good)
	expect(t, w, http.StatusOK)
	w = f.post(eventBody(t, "evt_old", "invoice.paid", "cus_x"), stripe.SignatureHeader(eventBody(t, "evt_old", "invoice.paid", "cus_x"), "whsec_old", testStart))
	expect(t, w, http.StatusOK)
	if n := f.count(`SELECT count(*) FROM billing_events`); n != 2 {
		t.Fatalf("billing_events has %d rows, want 2", n)
	}
}

func TestWebhookDedupe(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	body := eventBody(t, "evt_1", "customer.subscription.created", "cus_dup")
	for range 3 {
		expect(t, f.post(body, stripe.SignatureHeader(body, testSecret, testStart)), http.StatusOK)
	}
	if n := f.count(`SELECT count(*) FROM billing_events WHERE event_id='evt_1' AND customer_id='cus_dup' AND type='customer.subscription.created'`); n != 1 {
		t.Fatalf("billing_events has %d rows, want 1", n)
	}
	jobs := f.queuedSyncs()
	if len(jobs) != 1 || jobs[0].args != (SyncArgs{CustomerID: "cus_dup", EventID: "evt_1"}) {
		t.Fatalf("jobs = %+v, want one sync of cus_dup", jobs)
	}
	var queue string
	var attempts int
	if err := f.pool.QueryRow(t.Context(), `SELECT queue, max_attempts FROM river_job WHERE id=$1`, jobs[0].id).Scan(&queue, &attempts); err != nil {
		t.Fatal(err)
	}
	if queue != "default" || attempts != 20 {
		t.Fatalf("queue %q, max_attempts %d", queue, attempts)
	}
}

func TestWebhookUnknownType(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	body := eventBody(t, "evt_other", "customer.created", "cus_x")
	w := f.post(body, stripe.SignatureHeader(body, testSecret, testStart))
	expect(t, w, http.StatusOK)
	if n := f.count(`SELECT count(*) FROM billing_events`); n != 0 {
		t.Fatalf("an unhandled type wrote %d rows", n)
	}
	if n := len(f.queuedSyncs()); n != 0 {
		t.Fatalf("an unhandled type queued %d jobs", n)
	}
}

func TestWebhookBodyLimit(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	body := []byte(`{"id":"evt_big","type":"invoice.paid","data":{"object":{"pad":"` + strings.Repeat("x", 256<<10) + `"}}}`)
	w := f.post(body, stripe.SignatureHeader(body, testSecret, testStart))
	expect(t, w, http.StatusRequestEntityTooLarge)
}

func TestWebhookDatabaseError(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE billing_events RENAME TO billing_events_gone`); err != nil {
		t.Fatal(err)
	}
	body := eventBody(t, "evt_db", "invoice.paid", "cus_x")
	w := f.post(body, stripe.SignatureHeader(body, testSecret, testStart))
	expect(t, w, http.StatusInternalServerError)
}

func TestWebhookNeedsJobsAndSecret(t *testing.T) {
	f := newFixture(t, fixtureOptions{noJobs: true})
	body := eventBody(t, "evt_none", "invoice.paid", "cus_x")
	if w := f.post(body, stripe.SignatureHeader(body, testSecret, testStart)); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want no webhook route", w.Code)
	}
	// A second Routes call does not log again.
	f.svc.Routes(f.auth)(http.NewServeMux())
	if n := strings.Count(f.logs.String(), "webhook route is not registered"); n != 1 {
		t.Fatalf("the error was logged %d times, want once: %s", n, f.logs)
	}
}

func TestWebhookBearerHasNoEffect(t *testing.T) {
	f := newFixture(t, fixtureOptions{sessionOnly: true})
	uid := f.user("bearer@example.com", "")
	_, token, err := f.keys.Create(t.Context(), uid, apikeysInput())
	if err != nil {
		t.Fatal(err)
	}
	body := eventBody(t, "evt_bearer", "invoice.paid", "cus_x")
	h := http.Header{"Stripe-Signature": {stripe.SignatureHeader(body, testSecret, testStart)}}
	w := f.do(call{method: "POST", path: WebhookPath, body: string(body), header: h, bearer: token})
	expect(t, w, http.StatusOK)
}

func apikeysInput() apikeys.CreateInput {
	return apikeys.CreateInput{Name: "test key", Scope: apikeys.ScopeWrite}
}

func signed(body []byte) string { return stripe.SignatureHeader(body, testSecret, testStart) }
