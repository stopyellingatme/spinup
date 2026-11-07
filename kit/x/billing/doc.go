// Package billing sells subscriptions through Stripe and gates features by plan (ADR 0032).
//
// A webhook records each Stripe event and queues a sync job. The sync fetches
// the state of the customer from Stripe, so the event payload is never the
// source of truth. The sync sets the effective plan of the user, the apikeys
// plan, and the role. [Service.Require] and [Service.Has] gate features by
// the effective plan. [Service.ReportUsage] sends the API key calls of each
// metered user to Stripe Billing Meters.
package billing
