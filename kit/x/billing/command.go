package billing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

const commandUsage = "billing check | billing status <email> | billing sync <email> | billing report-usage"

// Command returns the noninteractive operator command "billing" (ADR 0032 section 14).
func (s *Service) Command() func(context.Context, []string) error {
	return func(ctx context.Context, args []string) error { return s.runCommand(ctx, args, os.Stdout) }
}

func commandError(action, why string) error {
	return fmt.Errorf("%s: %s.\nTry: %s", action, why, commandUsage)
}

func (s *Service) runCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError("billing", "no action was given")
	}
	name, rest := args[0], args[1:]
	action := "billing " + name
	want := 0
	switch name {
	case "check", "report-usage":
	case "status", "sync":
		want = 1
	default:
		return commandError("billing", fmt.Sprintf("unknown action %q", name))
	}
	if len(rest) < want {
		return commandError(action, "missing the email argument")
	}
	if len(rest) > want {
		return commandError(action, "unexpected argument "+rest[want])
	}
	ctx = audit.WithActor(ctx, audit.Operator())
	var text string
	var err error
	switch name {
	case "check":
		return s.check(ctx, out)
	case "status":
		text, err = s.status(ctx, rest[0])
	case "sync":
		text, err = s.syncUser(ctx, rest[0])
	case "report-usage":
		var n int
		n, err = s.reportUsage(ctx)
		text = fmt.Sprintf("sent %d meter events\n", n)
		if err != nil {
			_, _ = io.WriteString(out, text)
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if _, err := io.WriteString(out, text); err != nil {
		return commandError(action, "cannot write the output: "+err.Error())
	}
	return nil
}

// firstLine returns the first line of an error, without its Try: line.
func firstLine(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	return line
}

// check prints one line for each check and fails when a check fails.
func (s *Service) check(ctx context.Context, out io.Writer) error {
	failed := 0
	line := func(ok bool, format string, args ...any) {
		state := "ok  "
		if !ok {
			state = "FAIL"
			failed++
		}
		_, _ = fmt.Fprintf(out, "%s  %s\n", state, fmt.Sprintf(format, args...))
	}
	if s.client == nil {
		line(false, "Stripe key: STRIPE_SECRET_KEY is not set")
		return fmt.Errorf("billing check: %d checks failed.\nTry: set STRIPE_SECRET_KEY, then run billing check again", failed)
	}
	if err := s.client.CheckKey(ctx); err != nil {
		line(false, "Stripe key: %s", firstLine(err))
	} else {
		line(true, "Stripe key")
	}
	keys := s.cfg.lookupKeys()
	if len(keys) > 0 {
		prices, err := s.listPrices(ctx, keys)
		if err != nil {
			line(false, "prices: %s", firstLine(err))
		} else {
			for _, key := range keys {
				i := slices.IndexFunc(prices, func(p stripe.Price) bool { return p.LookupKey == key })
				switch {
				case i < 0:
					line(false, "price %s: no active price has this lookup key", key)
				case prices[i].Recurring == nil:
					line(false, "price %s: the price is not recurring", key)
				default:
					line(true, "price %s (%s, every %d %s, %s)", key, prices[i].ID, prices[i].Recurring.IntervalCount, prices[i].Recurring.Interval, usageType(prices[i].Recurring.UsageType))
				}
			}
		}
	}
	var meters []string
	for _, p := range s.cfg.Plans {
		if p.Meter != "" && !slices.Contains(meters, p.Meter) {
			meters = append(meters, p.Meter)
		}
	}
	if len(meters) > 0 {
		list, err := s.client.ListMeters(ctx)
		if err != nil {
			line(false, "meters: %s", firstLine(err))
		} else {
			for _, name := range meters {
				i := slices.IndexFunc(list, func(m stripe.Meter) bool { return m.EventName == name })
				switch {
				case i < 0:
					line(false, "meter %s: no meter has this event name", name)
				case list[i].Status != "active":
					line(false, "meter %s: the meter is %s, not active", name, list[i].Status)
				default:
					line(true, "meter %s (%s)", name, list[i].ID)
				}
			}
		}
	}
	if len(s.secrets) == 0 {
		line(false, "webhook secret: not set")
	} else {
		line(true, "webhook secret (%d set)", len(s.secrets))
	}
	if failed > 0 {
		return fmt.Errorf("billing check: %d checks failed.\nTry: fix the failed items in the Stripe Dashboard or the secrets, then run billing check again", failed)
	}
	return nil
}

func usageType(t string) string {
	if t == "" {
		return "licensed"
	}
	return t
}

// commandUser returns the ID of the user with the normalized email.
func (s *Service) commandUser(ctx context.Context, email string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE email=$1`, strings.ToLower(strings.TrimSpace(email))).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrUserNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("billing: cannot find the user: %w\nTry: check the database connection", err)
	}
	return id, nil
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

// status prints the customer, the subscriptions, and the entitlement of the user.
func (s *Service) status(ctx context.Context, email string) (string, error) {
	id, err := s.commandUser(ctx, email)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	customer, err := s.customerOf(ctx, id)
	if err != nil {
		return "", fmt.Errorf("billing: cannot read the customer: %w\nTry: check the database connection", err)
	}
	if customer == "" {
		customer = "none"
	}
	fmt.Fprintf(&b, "user\t%s\t%d\n", strings.ToLower(strings.TrimSpace(email)), id)
	fmt.Fprintf(&b, "customer\t%s\n", customer)
	rows, err := s.pool.Query(ctx, `SELECT id, status, plan, price_lookup_key, current_period_end, cancel_at_period_end FROM billing_subscriptions WHERE user_id=$1 ORDER BY id`, id)
	if err != nil {
		return "", fmt.Errorf("billing: cannot read the subscriptions: %w\nTry: check the database connection", err)
	}
	type subRow struct {
		id, status, plan, key string
		end                   *time.Time
		cancel                bool
	}
	subs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (subRow, error) {
		var r subRow
		err := row.Scan(&r.id, &r.status, &r.plan, &r.key, &r.end, &r.cancel)
		return r, err
	})
	if err != nil {
		return "", fmt.Errorf("billing: cannot read the subscriptions: %w\nTry: check the database connection", err)
	}
	for _, r := range subs {
		plan := r.plan
		if plan == "" {
			plan = "-"
		}
		fmt.Fprintf(&b, "subscription\t%s\t%s\t%s\t%s\tperiod_end=%s\tcancel_at_period_end=%t\n", r.id, r.status, plan, r.key, formatTime(r.end), r.cancel)
	}
	e, err := s.Entitlement(ctx, id)
	if err != nil {
		return "", err
	}
	status := e.Status
	if status == "" {
		status = "-"
	}
	fmt.Fprintf(&b, "entitlement\t%s\tstatus=%s\tfeatures=%s\n", e.Plan, status, strings.Join(e.Features, ","))
	return b.String(), nil
}

// syncUser runs Sync for the customer of the user.
func (s *Service) syncUser(ctx context.Context, email string) (string, error) {
	id, err := s.commandUser(ctx, email)
	if err != nil {
		return "", err
	}
	customer, err := s.customerOf(ctx, id)
	if err != nil {
		return "", fmt.Errorf("billing: cannot read the customer: %w\nTry: check the database connection", err)
	}
	if customer == "" {
		return "", errors.New("billing: the user has no Stripe customer.\nTry: subscribe on /pricing first, or check the email")
	}
	if err := s.Sync(ctx, customer); err != nil {
		return "", err
	}
	e, err := s.Entitlement(ctx, id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("synced %s\t%s\tplan=%s\n", strings.ToLower(strings.TrimSpace(email)), customer, e.Plan), nil
}
