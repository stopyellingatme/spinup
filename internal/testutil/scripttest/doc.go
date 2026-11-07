// Package scripttest runs the CLI scripts in testdata/script.
//
// The scripts can use these commands in addition to the testscript built-ins:
//
//   - dofake start: runs the DigitalOcean API fake and points the CLI at it.
//   - mkversion <version> [overlay-dir]: writes a synthetic template version under
//     $WORK/templates for SPINUP_TEMPLATES_DIR (see makeVersion).
//   - stripefake start <secret> <webhook-url>: runs the Stripe fake of the kit
//     (kit/x/billing/stripe/stripefake) on 127.0.0.1. The fake sends its signed
//     webhook events to <webhook-url>. The catalog has the licensed prices free and
//     pro_monthly, the metered price pro_metered, and the meter api_calls. The command
//     sets STRIPE_API_BASE, STRIPE_SECRET_KEY=sk_test_fake, and STRIPE_WEBHOOK_SECRET
//     for the next commands. The fake stops at the end of the script. The fake
//     numbers its objects in order, so the first Checkout session is cs_test_1 and
//     the first subscription is sub_1.
//   - stripefake complete <session-id>: completes a Checkout session, prints the
//     subscription ID, and sends checkout.session.completed and
//     customer.subscription.created.
//   - stripefake status <sub-id> <status>: changes the status of a subscription and
//     sends customer.subscription.updated, or customer.subscription.deleted for
//     canceled.
//   - stripefake resend <event-id>: sends a recorded event again, with the same ID
//     and a new signature, as Stripe does on a retry.
//   - stripefake meter-events: prints one line for each accepted meter event:
//     "<event name> <customer> <identifier> <value>".
//   - stripefake events: prints one line for each sent event: "<id> <type> <customer>".
//
// With "!", stripefake complete, status, and resend expect a failed webhook delivery.
package scripttest
