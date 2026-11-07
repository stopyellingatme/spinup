package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// CreateCustomer creates a customer with the supplied idempotency key.
func (c *Client) CreateCustomer(ctx context.Context, p CustomerParams, idempotencyKey string) (*Customer, error) {
	form := url.Values{"email": {p.Email}, "name": {p.Name}}
	metadata(form, p.Metadata)
	var result Customer
	if err := c.request(ctx, http.MethodPost, "/v1/customers", form, idempotencyKey, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreateCheckoutSession creates a hosted subscription checkout.
func (c *Client) CreateCheckoutSession(ctx context.Context, p CheckoutParams, idempotencyKey string) (*CheckoutSession, error) {
	form := url.Values{"mode": {"subscription"}, "ui_mode": {"hosted_page"}, "customer": {p.Customer}, "line_items[0][price]": {p.Price}, "client_reference_id": {p.ClientReferenceID}, "success_url": {p.SuccessURL}, "cancel_url": {p.CancelURL}}
	if !p.Metered {
		form.Set("line_items[0][quantity]", "1")
	}
	for i, id := range p.MeteredPrices {
		form.Set("line_items["+strconv.Itoa(i+1)+"][price]", id)
	}
	if p.TrialDays > 0 {
		form.Set("subscription_data[trial_period_days]", strconv.Itoa(p.TrialDays))
	}
	if p.AutomaticTax {
		form.Set("automatic_tax[enabled]", "true")
	}
	if p.AllowPromotionCodes {
		form.Set("allow_promotion_codes", "true")
	}
	metadata(form, p.Metadata)
	var result CheckoutSession
	if err := c.request(ctx, http.MethodPost, "/v1/checkout/sessions", form, idempotencyKey, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetCheckoutSession fetches a Checkout session.
func (c *Client) GetCheckoutSession(ctx context.Context, id string) (*CheckoutSession, error) {
	var result CheckoutSession
	if err := c.request(ctx, http.MethodGet, "/v1/checkout/sessions/"+url.PathEscape(id), nil, "", &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreatePortalSession creates a hosted Customer Portal session.
func (c *Client) CreatePortalSession(ctx context.Context, customer, returnURL string) (*PortalSession, error) {
	var result PortalSession
	if err := c.request(ctx, http.MethodPost, "/v1/billing_portal/sessions", url.Values{"customer": {customer}, "return_url": {returnURL}}, "", &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// wirePrice accepts either an expanded product or a product ID.
type wirePrice struct {
	Price
	Product json.RawMessage `json:"product"`
}

func (p wirePrice) value() (Price, error) {
	result := p.Price
	var id string
	if json.Unmarshal(p.Product, &id) == nil {
		result.Product.ID = id
		return result, nil
	}
	if len(p.Product) > 0 {
		if err := json.Unmarshal(p.Product, &result.Product); err != nil {
			return Price{}, err
		}
	}
	return result, nil
}

type wireItem struct {
	SubscriptionItem
	Price wirePrice `json:"price"`
}

type wireSubscription struct {
	Subscription
	Items struct {
		Data    []wireItem `json:"data"`
		HasMore bool       `json:"has_more"`
	} `json:"items"`
}

// ListSubscriptions returns all subscriptions, including canceled subscriptions.
func (c *Client) ListSubscriptions(ctx context.Context, customer string) ([]Subscription, error) {
	rows, err := list(ctx, c, "/v1/subscriptions", url.Values{"customer": {customer}, "status": {"all"}}, func(s wireSubscription) string { return s.ID })
	if err != nil {
		return nil, err
	}
	result := make([]Subscription, 0, len(rows))
	for _, row := range rows {
		sub := row.Subscription
		items := row.Items.Data
		if row.Items.HasMore {
			// A subscription embeds its first 10 items. Read them all, so that a
			// declared price on a later item still selects its plan.
			items, err = list(ctx, c, "/v1/subscription_items", url.Values{"subscription": {sub.ID}}, func(i wireItem) string { return i.ID })
			if err != nil {
				return nil, err
			}
		}
		for _, item := range items {
			price, err := item.Price.value()
			if err != nil {
				return nil, decodeError("/v1/subscriptions")
			}
			value := item.SubscriptionItem
			value.Price = price
			sub.Items = append(sub.Items, value)
		}
		result = append(result, sub)
	}
	return result, nil
}

// ListPrices returns active prices with expanded products for the lookup keys.
func (c *Client) ListPrices(ctx context.Context, lookupKeys []string) ([]Price, error) {
	rows, err := list(ctx, c, "/v1/prices", url.Values{"lookup_keys[]": lookupKeys, "active": {"true"}, "expand[]": {"data.product"}}, func(p wirePrice) string { return p.ID })
	if err != nil {
		return nil, err
	}
	result := make([]Price, 0, len(rows))
	for _, row := range rows {
		price, err := row.value()
		if err != nil {
			return nil, decodeError("/v1/prices")
		}
		result = append(result, price)
	}
	return result, nil
}

func decodeError(path string) error {
	return fmt.Errorf("stripe GET %s failed: invalid product in the API response\nTry: check the API base URL and response", path)
}

// ListMeters returns the billing meters.
func (c *Client) ListMeters(ctx context.Context) ([]Meter, error) {
	return list(ctx, c, "/v1/billing/meters", url.Values{}, func(m Meter) string { return m.ID })
}

// CreateMeterEvent reports metered usage by a customer.
func (c *Client) CreateMeterEvent(ctx context.Context, e MeterEvent) error {
	form := url.Values{"event_name": {e.EventName}, "payload[stripe_customer_id]": {e.Customer}, "payload[value]": {strconv.FormatInt(e.Value, 10)}, "identifier": {e.Identifier}, "timestamp": {strconv.FormatInt(e.Timestamp.Unix(), 10)}}
	return c.request(ctx, http.MethodPost, "/v1/billing/meter_events", form, "", nil)
}

// CheckKey checks the key with the balance endpoint.
func (c *Client) CheckKey(ctx context.Context) error {
	return c.request(ctx, http.MethodGet, "/v1/balance", nil, "", nil)
}
