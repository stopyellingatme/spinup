package billing

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/jobs"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

// webhookLimit is the largest webhook body (ADR 0032 section 8.1).
const webhookLimit = 256 << 10

// syncAttempts is the number of attempts of a sync job.
const syncAttempts = 20

// handledTypes are the event types that queue a sync (ADR 0032 section 8.3).
var handledTypes = map[string]bool{
	"checkout.session.completed":               true,
	"checkout.session.async_payment_succeeded": true,
	"customer.subscription.created":            true,
	"customer.subscription.updated":            true,
	"customer.subscription.deleted":            true,
	"customer.subscription.paused":             true,
	"customer.subscription.resumed":            true,
	"invoice.paid":                             true,
	"invoice.payment_failed":                   true,
}

func problem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteProblem(w, r, httpx.Problem{Status: status, Detail: detail})
}

func received(w http.ResponseWriter) {
	_ = httpx.WriteJSON(w, http.StatusOK, struct {
		Received bool `json:"received"`
	}{true})
}

// webhook records a handled event and queues a sync in one transaction.
func (s *Service) webhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhookLimit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			problem(w, r, http.StatusRequestEntityTooLarge, "The webhook body exceeds 256 KiB. Try: send a Stripe event with a smaller object.")
			return
		}
		problem(w, r, http.StatusBadRequest, "The webhook body could not be read. Try: send the event again.")
		return
	}
	event, err := stripe.VerifyWebhook(body, r.Header.Get("Stripe-Signature"), s.secrets, s.now())
	if err != nil {
		s.logger.WarnContext(ctx, "billing: rejected a webhook", "reason", err.Error(), "request_id", httpx.RequestIDFrom(ctx))
		problem(w, r, http.StatusBadRequest, "The webhook signature is not valid. Try: check the webhook signing secret in the Stripe Dashboard.")
		return
	}
	if !handledTypes[event.Type] {
		received(w)
		return
	}
	customer := event.Customer()
	if err := s.recordEvent(ctx, event, customer); err != nil {
		s.logger.ErrorContext(ctx, "billing: cannot record a webhook event", "event_id", event.ID, "type", event.Type, "error", err, "request_id", httpx.RequestIDFrom(ctx))
		problem(w, r, http.StatusInternalServerError, "The event was not recorded. Try: Stripe retries the event. Check the server logs.")
		return
	}
	received(w)
}

// recordEvent inserts the event row and, for a new row, the sync job.
func (s *Service) recordEvent(ctx context.Context, event stripe.Event, customer string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		reason := ""
		if customer == "" {
			reason = "no customer in the event"
		}
		tag, err := tx.Exec(ctx, `INSERT INTO billing_events(event_id,type,customer_id,error) VALUES($1,$2,$3,$4) ON CONFLICT (event_id) DO NOTHING`, event.ID, event.Type, customer, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if customer == "" {
			s.logger.WarnContext(ctx, "billing: a webhook event has no customer, so no sync runs", "event_id", event.ID, "type", event.Type)
			return nil
		}
		_, err = s.jobs.InsertTx(ctx, tx, SyncArgs{CustomerID: customer, EventID: event.ID}, jobs.Queue(jobs.DefaultQueue), jobs.MaxAttempts(syncAttempts))
		return err
	})
}
