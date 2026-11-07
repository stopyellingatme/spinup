// Package billing sells subscriptions through Stripe and gates features by plan (ADR 0032).
// A webhook queues a sync job, and the sync fetches the state from Stripe.
package billing
