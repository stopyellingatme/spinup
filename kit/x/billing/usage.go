package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

// usageDays is the number of UTC days, today included, that a pass reads.
const usageDays = 30

// ReportUsage runs one pass of the usage job (ADR 0032 section 13). For each
// user whose plan has a meter and who has a customer, it sends the new API
// key calls of each of the last 30 UTC days. A failed send stops the pass for
// that user, and ReportUsage returns the error after the other users.
func (s *Service) ReportUsage(ctx context.Context) error {
	_, err := s.reportUsage(ctx)
	return err
}

type meteredUser struct {
	id              int64
	meter, customer string
}

// reportUsage returns the number of meter events sent.
func (s *Service) reportUsage(ctx context.Context) (int, error) {
	if s.keys == nil {
		if s.hasMeter() {
			return 0, errors.New("billing: cannot report usage: the apikeys store is not set.\nTry: pass billing.WithAPIKeys(keyStore)")
		}
		return 0, nil
	}
	var plans, meters []string
	for _, p := range s.cfg.Plans {
		if p.Meter != "" {
			plans, meters = append(plans, p.Name), append(meters, p.Meter)
		}
	}
	if len(plans) == 0 {
		return 0, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT e.user_id, m.meter, c.customer_id
FROM billing_entitlements e
JOIN billing_customers c ON c.user_id = e.user_id
JOIN unnest($1::text[], $2::text[]) AS m(plan, meter) ON m.plan = e.plan
ORDER BY e.user_id`, plans, meters)
	if err != nil {
		return 0, usageError(err)
	}
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (meteredUser, error) {
		var u meteredUser
		err := row.Scan(&u.id, &u.meter, &u.customer)
		return u, err
	})
	if err != nil {
		return 0, usageError(err)
	}
	now := s.now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := today.AddDate(0, 0, -(usageDays - 1))
	sent := 0
	var failures []error
	for _, u := range users {
		n, err := s.reportUser(ctx, u, from, today, now)
		sent += n
		if err != nil {
			failures = append(failures, err)
		}
	}
	return sent, errors.Join(failures...)
}

func usageError(err error) error {
	return fmt.Errorf("billing: cannot read the usage: %w\nTry: check the database connection and run spinup db migrate up, then retry", err)
}

// reportUser sends the usage of one user and records each sent day.
//
// Each send has a checkpoint. The pass first commits the target total as
// pending, then sends the delta with an identifier that names the target, and
// then commits the target as reported. When the pass stops between the send and
// the second commit, the next pass sends the same event again, and Stripe
// dedupes it by its identifier. A session advisory lock for each user keeps two
// passes (the periodic job and billing report-usage) from sending the same delta.
func (s *Service) reportUser(ctx context.Context, u meteredUser, from, today, now time.Time) (int, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, usageError(err)
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('billing-usage:' || $1::bigint))`, u.id).Scan(&locked); err != nil {
		return 0, usageError(err)
	}
	if !locked {
		s.logger.InfoContext(ctx, "billing: another usage pass is sending the usage of this user", "user_id", u.id)
		return 0, nil
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext('billing-usage:' || $1::bigint))`, u.id); err != nil {
			s.logger.WarnContext(ctx, "billing: cannot release the usage lock", "user_id", u.id, "error", err)
		}
	}()
	rows, err := conn.Query(ctx, `SELECT g.day, sum(g.count)::bigint, COALESCE(max(r.reported), 0)::bigint, COALESCE(max(r.pending), 0)::bigint
FROM api_key_usage g
JOIN api_keys k ON k.id = g.key_id
LEFT JOIN billing_usage_reports r ON r.user_id = k.user_id AND r.day = g.day
WHERE k.user_id = $1 AND g.day >= $2::date AND g.day <= $3::date
GROUP BY g.day ORDER BY g.day`, u.id, from.Format(time.DateOnly), today.Format(time.DateOnly))
	if err != nil {
		return 0, usageError(err)
	}
	type dayRow struct {
		day                      time.Time
		total, reported, pending int64
	}
	days, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dayRow, error) {
		var d dayRow
		err := row.Scan(&d.day, &d.total, &d.reported, &d.pending)
		return d, err
	})
	if err != nil {
		return 0, usageError(err)
	}
	sent := 0
	for _, d := range days {
		day := time.Date(d.day.Year(), d.day.Month(), d.day.Day(), 0, 0, 0, 0, time.UTC)
		stamp := day.Add(24*time.Hour - time.Second)
		if day.Equal(today) {
			stamp = now
		}
		label := day.Format(time.DateOnly)
		send := func(from, target int64) error {
			event := stripe.MeterEvent{EventName: u.meter, Customer: u.customer, Value: target - from,
				Identifier: fmt.Sprintf("apikeys:%d:%s:%d", u.id, label, target), Timestamp: stamp}
			if err := s.reporter.Report(ctx, event); err != nil {
				return tryLine(fmt.Errorf("billing: cannot report the usage of user %d on %s: %w", u.id, label, err), "check the Stripe key and the meter, then run billing report-usage again")
			}
			if _, err := conn.Exec(ctx, `UPDATE billing_usage_reports SET reported = $3 WHERE user_id = $1 AND day = $2::date AND reported < $3`, u.id, label, target); err != nil {
				return usageError(err)
			}
			sent++
			return nil
		}
		// A pending target that was not confirmed: send the same event again.
		if d.pending > d.reported {
			if err := send(d.reported, d.pending); err != nil {
				return sent, err
			}
			d.reported = d.pending
		}
		if d.total <= d.reported {
			continue
		}
		if _, err := conn.Exec(ctx, `INSERT INTO billing_usage_reports(user_id, day, reported, pending) VALUES($1, $2::date, $3, $4)
ON CONFLICT (user_id, day) DO UPDATE SET pending = GREATEST(billing_usage_reports.pending, excluded.pending)`, u.id, label, d.reported, d.total); err != nil {
			return sent, usageError(err)
		}
		if err := send(d.reported, d.total); err != nil {
			return sent, err
		}
	}
	return sent, nil
}
