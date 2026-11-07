package stripefake

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

func route(method, path string) string {
	switch method + " " + path {
	case "GET /v1/balance":
		return "balance"
	case "POST /v1/customers":
		return "customer"
	case "POST /v1/checkout/sessions":
		return "checkout"
	case "POST /v1/billing_portal/sessions":
		return "portal"
	case "GET /v1/subscriptions":
		return "subscriptions"
	case "GET /v1/prices":
		return "prices"
	case "GET /v1/billing/meters":
		return "meters"
	case "POST /v1/billing/meter_events":
		return "meter_event"
	}
	if method == http.MethodGet && strings.HasPrefix(path, "/v1/checkout/sessions/") {
		return "session"
	}
	return ""
}

func allowed(endpoint, key string) bool {
	switch endpoint {
	case "customer":
		return key == "email" || key == "name" || metadataKey(key)
	case "checkout":
		return slices.Contains([]string{"mode", "ui_mode", "customer", "line_items[0][price]", "line_items[0][quantity]", "client_reference_id", "success_url", "cancel_url", "subscription_data[trial_period_days]", "automatic_tax[enabled]", "allow_promotion_codes"}, key) || metadataKey(key)
	case "portal":
		return key == "customer" || key == "return_url"
	case "subscriptions":
		return key == "customer" || key == "status" || key == "limit" || key == "starting_after"
	case "prices":
		return key == "lookup_keys[]" || key == "active" || key == "expand[]" || key == "limit" || key == "starting_after"
	case "meters":
		return key == "limit" || key == "starting_after"
	case "meter_event":
		return slices.Contains([]string{"event_name", "payload[stripe_customer_id]", "payload[value]", "identifier", "timestamp"}, key)
	}
	return false
}

func metadataKey(key string) bool {
	if !strings.HasPrefix(key, "metadata[") || !strings.HasSuffix(key, "]") {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(key, "metadata["), "]")
	return name != "" && !strings.ContainsAny(name, "[]")
}

func metadataValues(form url.Values) map[string]string {
	result := map[string]string{}
	for key := range form {
		if metadataKey(key) {
			result[key[len("metadata["):len(key)-1]] = form.Get(key)
		}
	}
	return result
}

func (f *Fake) dispatch(endpoint, path string, form url.Values) (any, *apiError) {
	switch endpoint {
	case "balance":
		return map[string]any{"object": "balance", "available": []any{}, "pending": []any{}, "livemode": false}, nil
	case "customer":
		customer := stripe.Customer{ID: f.id("cus_"), Email: form.Get("email"), Metadata: metadataValues(form)}
		f.customers[customer.ID] = customer
		return customer, nil
	case "checkout":
		return f.checkout(form)
	case "session":
		session, ok := f.sessions[strings.TrimPrefix(path, "/v1/checkout/sessions/")]
		if !ok {
			return nil, missing("session")
		}
		return sessionObject(session), nil
	case "portal":
		if _, ok := f.customers[form.Get("customer")]; !ok {
			return nil, missing("customer")
		}
		if !validURL(form.Get("return_url")) {
			return nil, invalid("url_invalid", "return_url", "An HTTP return URL is required")
		}
		id := f.id("bpc_")
		return stripe.PortalSession{ID: id, URL: "https://billing.stripe.fake/p/session/" + id}, nil
	case "subscriptions":
		return f.listSubscriptions(form)
	case "prices":
		return f.listPrices(form)
	case "meters":
		return page(f.o.Meters, form, func(m stripe.Meter) string { return m.ID })
	case "meter_event":
		return f.meterEvent(form)
	}
	return nil, invalid("invalid_request", "", "Unknown endpoint")
}

func validURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

func (f *Fake) checkout(form url.Values) (any, *apiError) {
	if form.Get("mode") != "subscription" || form.Get("ui_mode") != "hosted_page" {
		return nil, invalid("parameter_invalid_enum", "mode", "Checkout requires subscription mode and hosted_page UI")
	}
	if _, ok := f.customers[form.Get("customer")]; !ok {
		return nil, missing("customer")
	}
	var price stripe.Price
	found := false
	for _, p := range f.o.Prices {
		if p.ID == form.Get("line_items[0][price]") {
			price = p
			found = true
			break
		}
	}
	if !found {
		return nil, missing("price")
	}
	if !price.Active || price.Recurring == nil {
		return nil, invalid("parameter_invalid", "line_items[0][price]", "An active recurring price is required")
	}
	quantity, provided := form["line_items[0][quantity]"]
	if price.Recurring.UsageType == "metered" {
		if provided {
			return nil, invalid("parameter_invalid", "line_items[0][quantity]", "A metered price must not have a quantity")
		}
	} else if len(quantity) != 1 || quantity[0] != "1" {
		return nil, invalid("parameter_invalid", "line_items[0][quantity]", "A licensed price requires quantity 1")
	}
	if !validURL(form.Get("success_url")) || !validURL(form.Get("cancel_url")) {
		return nil, invalid("url_invalid", "success_url", "HTTP success and cancel URLs are required")
	}
	trial := 0
	if value := form.Get("subscription_data[trial_period_days]"); value != "" {
		days, err := strconv.Atoi(value)
		if err != nil || days < 0 || days > 730 {
			return nil, invalid("parameter_invalid_integer", "subscription_data[trial_period_days]", "Trial days must be between 0 and 730")
		}
		trial = days
	}
	for _, key := range []string{"automatic_tax[enabled]", "allow_promotion_codes"} {
		if value := form.Get(key); value != "" && value != "true" && value != "false" {
			return nil, invalid("parameter_invalid_boolean", key, "A boolean value is required")
		}
	}
	id := f.id("cs_test_")
	state := &checkoutState{session: stripe.CheckoutSession{ID: id, URL: "https://checkout.stripe.fake/c/pay/" + id, Customer: form.Get("customer"), ClientReferenceID: form.Get("client_reference_id"), Status: "open", PaymentStatus: "unpaid"}, price: price, trialDays: trial, metadata: metadataValues(form)}
	f.sessions[id] = state
	return sessionObject(state), nil
}

func (f *Fake) listSubscriptions(form url.Values) (any, *apiError) {
	if _, ok := f.customers[form.Get("customer")]; !ok {
		return nil, missing("customer")
	}
	status := form.Get("status")
	if status != "" && !slices.Contains([]string{"all", "active", "past_due", "unpaid", "canceled", "paused", "trialing", "incomplete", "incomplete_expired"}, status) {
		return nil, invalid("parameter_invalid_enum", "status", "Invalid subscription status")
	}
	rows := make([]map[string]any, 0)
	for _, sub := range f.subscriptions {
		if sub.Customer != form.Get("customer") {
			continue
		}
		if status == "" && sub.Status == "canceled" {
			continue
		}
		if status != "" && status != "all" && sub.Status != status {
			continue
		}
		rows = append(rows, subscriptionObject(sub))
	}
	return page(rows, form, func(row map[string]any) string { return row["id"].(string) })
}

func (f *Fake) listPrices(form url.Values) (any, *apiError) {
	if active := form.Get("active"); active != "" && active != "true" && active != "false" {
		return nil, invalid("parameter_invalid_boolean", "active", "A boolean value is required")
	}
	for _, expand := range form["expand[]"] {
		if expand != "data.product" {
			return nil, invalid("parameter_invalid", "expand", "Only data.product can be expanded")
		}
	}
	rows := make([]stripe.Price, 0)
	for _, price := range f.o.Prices {
		if len(form["lookup_keys[]"]) > 0 && !slices.Contains(form["lookup_keys[]"], price.LookupKey) {
			continue
		}
		if active := form.Get("active"); active != "" && (active == "true") != price.Active {
			continue
		}
		rows = append(rows, price)
	}
	return page(rows, form, func(p stripe.Price) string { return p.ID })
}

func page[T any](rows []T, form url.Values, id func(T) string) (any, *apiError) {
	limit := 10
	if value := form.Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100 {
			return nil, invalid("parameter_invalid_integer", "limit", "Limit must be between 1 and 100")
		}
		limit = n
	}
	start := 0
	if cursor := form.Get("starting_after"); cursor != "" {
		found := false
		for i, row := range rows {
			if id(row) == cursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, missing("starting_after")
		}
	}
	end := min(start+limit, len(rows))
	data := rows[start:end]
	if data == nil {
		data = []T{}
	}
	return map[string]any{"object": "list", "data": data, "has_more": end < len(rows)}, nil
}

func (f *Fake) meterEvent(form url.Values) (any, *apiError) {
	customer := form.Get("payload[stripe_customer_id]")
	if _, ok := f.customers[customer]; !ok {
		return nil, missing("customer")
	}
	active := false
	for _, meter := range f.o.Meters {
		if meter.EventName == form.Get("event_name") && meter.Status == "active" {
			active = true
			break
		}
	}
	if !active {
		return nil, invalid("resource_missing", "event_name", "An active billing meter is required")
	}
	now := f.o.Now()
	stamp := now
	if raw := form.Get("timestamp"); raw != "" {
		seconds, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, invalid("parameter_invalid_integer", "timestamp", "A Unix timestamp is required")
		}
		stamp = time.Unix(seconds, 0)
	}
	if stamp.Before(now.Add(-35*24*time.Hour)) || stamp.After(now.Add(5*time.Minute)) {
		return nil, invalid("parameter_invalid", "timestamp", "Timestamp is outside the meter event window")
	}
	value, err := strconv.ParseInt(form.Get("payload[value]"), 10, 64)
	if err != nil {
		return nil, invalid("parameter_invalid_integer", "payload[value]", "An integer value is required")
	}
	identifier := form.Get("identifier")
	if identifier == "" {
		return nil, invalid("parameter_missing", "identifier", "An identifier is required")
	}
	event := stripe.MeterEvent{EventName: form.Get("event_name"), Customer: customer, Identifier: identifier, Value: value, Timestamp: stamp}
	if previous, ok := f.meterSeen[identifier]; !ok || now.Sub(previous) >= 24*time.Hour {
		f.meterSeen[identifier] = now
		f.meterEvents = append(f.meterEvents, event)
	}
	return map[string]any{"object": "billing.meter_event", "event_name": event.EventName, "identifier": identifier, "timestamp": stamp.Unix(), "payload": map[string]string{"stripe_customer_id": customer, "value": strconv.FormatInt(value, 10)}}, nil
}
