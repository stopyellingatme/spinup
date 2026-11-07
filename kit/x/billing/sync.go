package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

// liveStatuses are the subscription statuses that give a plan (ADR 0032 section 9.4).
var liveStatuses = []string{"active", "trialing", "past_due"}

func live(status string) bool { return slices.Contains(liveStatuses, status) }

// errUnknownCustomer means no user has the Stripe customer.
var errUnknownCustomer = errors.New("billing: the Stripe customer belongs to no user.\nTry: check that the customer was created by this app")

// Sync fetches the subscriptions of the customer from Stripe and sets the
// effective plan of its user (ADR 0032 section 9). An unknown customer logs
// a warning and returns an error. The sync job instead marks its event row
// with the error "unknown customer" and succeeds.
func (s *Service) Sync(ctx context.Context, customerID string) error {
	return s.sync(ctx, customerID, "")
}

func syncError(customer string, err error) error {
	return tryLine(fmt.Errorf("billing: cannot sync the customer %s: %w", customer, err), "check the Stripe key and the database, then run billing sync again")
}

// tryLine adds a Try: line to err when it has none.
func tryLine(err error, try string) error {
	if strings.Contains(err.Error(), "\nTry:") {
		return err
	}
	return fmt.Errorf("%w\nTry: %s", err, try)
}

// subscriptionRow is one billing_subscriptions row.
type subscriptionRow struct {
	id, status, lookupKey, plan string
	periodEnd, trialEnd         *time.Time
	cancelAtPeriodEnd           bool
}

func unixTime(seconds int64) *time.Time {
	if seconds <= 0 {
		return nil
	}
	t := time.Unix(seconds, 0).UTC()
	return &t
}

// rowOf maps a Stripe subscription. The plan comes from the licensed item
// whose price lookup key a plan declares. A subscription of a plan with a
// meter also has the metered item of the plan, which maps to the same plan.
// The first metered item decides only when no licensed item matches (a plan
// of metered prices only).
func (s *Service) rowOf(sub stripe.Subscription) subscriptionRow {
	row := subscriptionRow{id: sub.ID, status: sub.Status, cancelAtPeriodEnd: sub.CancelAtPeriodEnd, trialEnd: unixTime(sub.TrialEnd)}
	for _, metered := range []bool{false, true} {
		for _, item := range sub.Items {
			if isMetered(item.Price) != metered {
				continue
			}
			if p, ok := s.cfg.planOfPrice(item.Price.LookupKey); ok {
				row.plan, row.lookupKey, row.periodEnd = p.Name, item.Price.LookupKey, unixTime(item.CurrentPeriodEnd)
				return row
			}
		}
	}
	if len(sub.Items) > 0 {
		row.lookupKey, row.periodEnd = sub.Items[0].Price.LookupKey, unixTime(sub.Items[0].CurrentPeriodEnd)
	}
	return row
}

// effective returns the effective plan and its status (ADR 0032 section 9.4).
// With no live subscription, the plan is the default plan and the status is
// the status of the first subscription that Stripe returned, or "".
func (s *Service) effective(rows []subscriptionRow) (Plan, string) {
	best, status := -1, ""
	for _, row := range rows {
		if !live(row.status) || row.plan == "" {
			continue
		}
		if r := s.cfg.rank(row.plan); r > best {
			best, status = r, row.status
		}
	}
	if best >= 0 {
		return s.cfg.Plans[best], status
	}
	p, _ := s.cfg.Plan(s.cfg.DefaultPlan)
	if len(rows) > 0 {
		status = rows[0].status
	}
	return p, status
}

func (s *Service) sync(ctx context.Context, customer, eventID string) error {
	if s.client == nil {
		return ErrNotConfigured
	}
	source := audit.SourceAPI
	if a, ok := audit.ActorFrom(ctx); ok && a.Source != "" {
		source = a.Source
	}
	actor := audit.System(source, SyncArgs{}.Kind())
	ctx = audit.WithActor(ctx, actor)
	err := audit.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('billing:' || $1))`, customer); err != nil {
			return err
		}
		var userID int64
		err := tx.QueryRow(ctx, `SELECT user_id FROM billing_customers WHERE customer_id=$1`, customer).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			s.logger.WarnContext(ctx, "billing: the sync found no user for the Stripe customer. Try: check that the customer was created by this app.", "customer", customer, "event_id", eventID)
			if eventID != "" {
				_, err = tx.Exec(ctx, `UPDATE billing_events SET processed_at=now(), error='unknown customer' WHERE event_id=$1`, eventID)
				return err
			}
			return errUnknownCustomer
		}
		if err != nil {
			return err
		}
		// The list comes after the lock, so the last sync writes the newest state.
		subs, err := s.client.ListSubscriptions(ctx, customer)
		if err != nil {
			return err
		}
		rows := make([]subscriptionRow, 0, len(subs))
		ids := make([]string, 0, len(subs))
		for _, sub := range subs {
			row := s.rowOf(sub)
			rows = append(rows, row)
			ids = append(ids, row.id)
			if _, err := tx.Exec(ctx, `INSERT INTO billing_subscriptions(id,user_id,customer_id,status,price_lookup_key,plan,current_period_end,cancel_at_period_end,trial_end,synced_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
ON CONFLICT (id) DO UPDATE SET user_id=excluded.user_id,customer_id=excluded.customer_id,status=excluded.status,price_lookup_key=excluded.price_lookup_key,
 plan=excluded.plan,current_period_end=excluded.current_period_end,cancel_at_period_end=excluded.cancel_at_period_end,trial_end=excluded.trial_end,synced_at=now()
WHERE (billing_subscriptions.user_id,billing_subscriptions.customer_id,billing_subscriptions.status,billing_subscriptions.price_lookup_key,billing_subscriptions.plan,
 billing_subscriptions.current_period_end,billing_subscriptions.cancel_at_period_end,billing_subscriptions.trial_end)
 IS DISTINCT FROM (excluded.user_id,excluded.customer_id,excluded.status,excluded.price_lookup_key,excluded.plan,excluded.current_period_end,excluded.cancel_at_period_end,excluded.trial_end)`,
				row.id, userID, customer, row.status, row.lookupKey, row.plan, row.periodEnd, row.cancelAtPeriodEnd, row.trialEnd); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM billing_subscriptions WHERE customer_id=$1 AND NOT (id = ANY($2::text[]))`, customer, ids); err != nil {
			return err
		}
		plan, status := s.effective(rows)
		if err := s.applyPlan(ctx, tx, userID, plan, status, actor); err != nil {
			return err
		}
		if eventID != "" {
			if _, err := tx.Exec(ctx, `UPDATE billing_events SET processed_at=now(), error='' WHERE event_id=$1`, eventID); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, errUnknownCustomer) {
		return err
	}
	if eventID != "" {
		// Keep the cause on the event row for the operator. The job retries.
		msg := err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if _, uerr := s.pool.Exec(context.WithoutCancel(ctx), `UPDATE billing_events SET error=$2 WHERE event_id=$1`, eventID, msg); uerr != nil {
			s.logger.WarnContext(ctx, "billing: cannot record the sync error on the event", "event_id", eventID, "error", uerr)
		}
	}
	return syncError(customer, err)
}

// applyPlan writes the entitlement and, when the plan or its features change,
// the apikeys plan, the role, and the audit record (ADR 0032 section 9.5).
func (s *Service) applyPlan(ctx context.Context, tx pgx.Tx, userID int64, plan Plan, status string, actor audit.Actor) error {
	features := plan.Features
	if features == nil {
		features = []string{}
	}
	oldName, _ := s.cfg.Plan(s.cfg.DefaultPlan)
	old := Entitlement{Plan: oldName.Name, Features: oldName.Features}
	err := tx.QueryRow(ctx, `SELECT plan,features,status FROM billing_entitlements WHERE user_id=$1 FOR UPDATE`, userID).Scan(&old.Plan, &old.Features, &old.Status)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	exists := err == nil
	planChanged := old.Plan != plan.Name || !slices.Equal(old.Features, features)
	if exists && !planChanged && old.Status == status {
		return nil
	}
	if !exists && !planChanged && status == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billing_entitlements(user_id,plan,features,status,updated_at) VALUES($1,$2,$3,$4,now())
ON CONFLICT (user_id) DO UPDATE SET plan=excluded.plan,features=excluded.features,status=excluded.status,updated_at=now()`, userID, plan.Name, features, status); err != nil {
		return err
	}
	if !planChanged {
		return nil
	}
	if s.keys != nil {
		target := plan.APIKeysPlan
		if target == "" {
			target = s.keys.DefaultPlan()
		}
		if err := s.keys.SetPlanForUser(ctx, tx, userID, target); err != nil {
			return err
		}
	}
	oldPlan, _ := s.cfg.Plan(old.Plan)
	if (oldPlan.Role != "" || plan.Role != "") && s.roles != nil {
		if err := s.applyRole(ctx, tx, userID, plan); err != nil {
			return err
		}
	}
	var hasAudit bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.audit_events') IS NOT NULL`).Scan(&hasAudit); err != nil {
		return err
	}
	if !hasAudit {
		return nil
	}
	changes := map[string]any{"plan": map[string]string{"old": old.Plan, "new": plan.Name}}
	if !slices.Equal(old.Features, features) {
		changes["features"] = map[string][]string{"old": nonNil(old.Features), "new": features}
	}
	data, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Event{Action: "billing.plan_changed", Table: "billing_entitlements", RecordID: strconv.FormatInt(userID, 10), OwnerID: userID, Changes: data, Actor: actor})
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// applyRole sets the role of the plan, or the default role. It changes only
// the default role or a role that a billing plan grants, and never the admin role.
func (s *Service) applyRole(ctx context.Context, tx pgx.Tx, userID int64, plan Plan) error {
	var current string
	if err := tx.QueryRow(ctx, `SELECT role FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&current); err != nil {
		return err
	}
	effective := current
	if effective == "" {
		effective = s.roles.Default
	}
	if effective == s.roles.Admin {
		return nil
	}
	granted := slices.ContainsFunc(s.cfg.Plans, func(p Plan) bool { return p.Role != "" && p.Role == effective })
	if effective != s.roles.Default && !granted {
		return nil
	}
	target := plan.Role
	if target == "" {
		target = s.roles.Default
	}
	if target == effective {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE users SET role=$2 WHERE id=$1`, userID, target)
	return err
}
