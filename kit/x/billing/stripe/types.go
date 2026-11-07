package stripe

import (
	"encoding/json"
	"time"
)

// APIVersion is the Stripe API version used by this client.
const APIVersion = "2026-10-28.endive"

// DefaultBaseURL is the Stripe API origin.
const DefaultBaseURL = "https://api.stripe.com"

// Customer is a Stripe customer.
type Customer struct {
	// ID identifies the customer.
	ID string `json:"id"`
	// Email is the customer email address.
	Email string `json:"email"`
	// Metadata contains application fields.
	Metadata map[string]string `json:"metadata"`
}

// CustomerParams supplies the fields of a new customer.
type CustomerParams struct {
	// Email and Name describe the customer.
	Email, Name string
	// Metadata contains application fields.
	Metadata map[string]string
}

// CheckoutParams supplies the fields of a hosted subscription checkout.
type CheckoutParams struct {
	// Customer, Price, ClientReferenceID, SuccessURL, and CancelURL identify the checkout and its redirects.
	Customer, Price, ClientReferenceID, SuccessURL, CancelURL string
	// TrialDays is the trial duration in days.
	TrialDays int
	// AutomaticTax and AllowPromotionCodes enable the corresponding Stripe options.
	AutomaticTax, AllowPromotionCodes bool
	// Metadata contains application fields.
	Metadata map[string]string
	// Metered omits the quantity for the Price line item, because Price is a metered price.
	Metered bool
	// MeteredPrices are further metered price IDs. Each is a line item
	// after Price, with no quantity (Stripe rejects a quantity on a metered price).
	MeteredPrices []string
}

// CheckoutSession is a Stripe Checkout session.
type CheckoutSession struct {
	// ID identifies the session.
	ID string `json:"id"`
	// URL opens the hosted checkout.
	URL string `json:"url"`
	// Customer identifies the customer.
	Customer string `json:"customer"`
	// ClientReferenceID identifies the application user.
	ClientReferenceID string `json:"client_reference_id"`
	// Status is the checkout status.
	Status string `json:"status"`
	// PaymentStatus is the payment status.
	PaymentStatus string `json:"payment_status"`
	// Subscription identifies the resulting subscription.
	Subscription string `json:"subscription"`
}

// PortalSession is a hosted Customer Portal session.
type PortalSession struct {
	// ID identifies the portal session.
	ID string `json:"id"`
	// URL opens the hosted Customer Portal.
	URL string `json:"url"`
}

// Price is a Stripe price with its product.
type Price struct {
	// ID identifies the price.
	ID string `json:"id"`
	// LookupKey is the application lookup key.
	LookupKey string `json:"lookup_key"`
	// Currency is the currency code.
	Currency string `json:"currency"`
	// Nickname is the price description.
	Nickname string `json:"nickname"`
	// UnitAmount is the amount in the smallest currency unit.
	UnitAmount int64 `json:"unit_amount"`
	// Active permits new purchases.
	Active bool `json:"active"`
	// Recurring describes the recurring charge, or is nil for a single payment.
	Recurring *Recurring `json:"recurring"`
	// Product describes the associated product.
	Product Product `json:"product"`
}

// Recurring describes a recurring price and its billing meter.
type Recurring struct {
	// Interval is the billing interval.
	Interval string `json:"interval"`
	// IntervalCount is the number of intervals per charge.
	IntervalCount int `json:"interval_count"`
	// UsageType is licensed or metered.
	UsageType string `json:"usage_type"`
	// Meter identifies the associated billing meter.
	Meter string `json:"meter"`
}

// Product is the product of a price.
type Product struct {
	// ID identifies the product.
	ID string `json:"id"`
	// Name is the product name.
	Name string `json:"name"`
	// Description describes the product.
	Description string `json:"description"`
}

// Subscription is a customer's subscription.
type Subscription struct {
	// ID identifies the subscription.
	ID string `json:"id"`
	// Customer identifies the customer.
	Customer string `json:"customer"`
	// Status is the subscription status.
	Status string `json:"status"`
	// CancelAtPeriodEnd indicates cancellation at the current period end.
	CancelAtPeriodEnd bool `json:"cancel_at_period_end"`
	// TrialEnd is the trial end as a Unix timestamp.
	TrialEnd int64 `json:"trial_end"`
	// Items contains the subscription prices and periods.
	Items []SubscriptionItem `json:"items"`
}

// SubscriptionItem is a price and its current billing period.
type SubscriptionItem struct {
	// ID identifies the item.
	ID string `json:"id"`
	// Price describes the subscribed price.
	Price Price `json:"price"`
	// CurrentPeriodEnd is the period end as a Unix timestamp.
	CurrentPeriodEnd int64 `json:"current_period_end"`
}

// Meter is a Stripe Billing Meter.
type Meter struct {
	// ID identifies the meter.
	ID string `json:"id"`
	// EventName selects the usage event.
	EventName string `json:"event_name"`
	// DisplayName is the meter name.
	DisplayName string `json:"display_name"`
	// Status is the meter status.
	Status string `json:"status"`
}

// MeterEvent records usage by a customer.
type MeterEvent struct {
	// EventName, Customer, and Identifier select the meter, customer, and deduplication key.
	EventName, Customer, Identifier string
	// Value is the usage increment.
	Value int64
	// Timestamp is the usage time.
	Timestamp time.Time
}

// Event is a Stripe webhook event.
type Event struct {
	// ID identifies the event.
	ID string `json:"id"`
	// Type names the event.
	Type string `json:"type"`
	// APIVersion is the webhook API version.
	APIVersion string `json:"api_version"`
	// Created is the creation time as a Unix timestamp.
	Created int64 `json:"created"`
	// Livemode indicates a live event.
	Livemode bool `json:"livemode"`
	// Data contains the complete event object.
	Data struct {
		// Object is the raw Stripe object.
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// Customer returns the customer ID in the event object.
func (e Event) Customer() string {
	var object struct {
		ID       string          `json:"id"`
		Object   string          `json:"object"`
		Customer json.RawMessage `json:"customer"`
	}
	if json.Unmarshal(e.Data.Object, &object) != nil {
		return ""
	}
	if object.Object == "customer" {
		return object.ID
	}
	var id string
	if json.Unmarshal(object.Customer, &id) == nil {
		return id
	}
	var customer struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(object.Customer, &customer) == nil {
		return customer.ID
	}
	return ""
}
