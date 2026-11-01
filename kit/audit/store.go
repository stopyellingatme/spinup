package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/db"
)

// actionPattern is the check constraint audit_events_action_check.
var actionPattern = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,63}$`)

// tryMigrate is the hint for a database without the audit schema.
const tryMigrate = "run the migrations (spinup db migrate up) with the audit module on (spinup module add audit)"

// dbError is a database failure of the package. Its text names the action and
// ends with a Try: line. A PostgreSQL error with a "Try:" hint (the audit
// functions raise these) gives its message and its hint.
type dbError struct {
	what string
	err  error
	try  string
}

func (e *dbError) Error() string {
	msg, try := e.err.Error(), e.try
	var pg *pgconn.PgError
	if errors.As(e.err, &pg) {
		msg = pg.Message
		switch {
		case strings.HasPrefix(pg.Hint, "Try:"):
			try = strings.TrimSpace(strings.TrimPrefix(pg.Hint, "Try:"))
		case pg.Code == "42P01" || pg.Code == "42883": // undefined_table, undefined_function
			try = tryMigrate
		}
	}
	return "audit: " + e.what + ": " + msg + "\nTry: " + try
}

func (e *dbError) Unwrap() error { return e.err }

func wrap(what string, err error, try string) error {
	if err == nil {
		return nil
	}
	return &dbError{what: what, err: err, try: try}
}

// Bind sets the actor of ctx as the transaction-local setting spinup.audit, so
// the triggers of the transaction read it. It returns ErrNotInTx when tx is a
// *pgxpool.Pool or a *pgx.Conn. Without an actor in ctx, it binds an empty
// object, and the triggers write unattributed events.
func Bind(ctx context.Context, tx Execer) error {
	switch tx.(type) {
	case nil:
		return errors.New("audit: Bind needs a transaction, but it got nil\nTry: pass the pgx.Tx of db.InTx, or use audit.InTx")
	case *pgxpool.Pool, *pgx.Conn:
		return ErrNotInTx
	}
	actor, _ := ActorFrom(ctx)
	value, err := json.Marshal(actor)
	if err != nil {
		return fmt.Errorf("audit: encode the actor: %w\nTry: report this error, because an Actor always encodes", err)
	}
	_, err = tx.Exec(ctx, `SELECT set_config('spinup.audit', $1, true)`, string(value))
	return wrap("bind the actor", err, "check the database connection and retry")
}

// InTx runs fn in a transaction (db.InTx) after Bind with the actor of ctx.
// It commits when fn returns nil and rolls back when fn or Bind fails.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	if pool == nil {
		return errors.New("audit: InTx needs a database pool, but it got nil\nTry: pass the pool from db.Open")
	}
	return db.InTx(ctx, pool, func(tx pgx.Tx) error {
		if err := Bind(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
}

// Record writes an event that is not a row change, such as auth.sign_in. It
// takes the actor from e.Actor, or from ActorFrom(ctx) when e.Actor.Kind is
// empty, and passes it to audit_append. It reads no setting, so q can be a pool.
// e.ID and e.OccurredAt are ignored: the database sets them.
func Record(ctx context.Context, q Execer, e Event) error {
	if q == nil {
		return errors.New("audit: Record needs a database pool or a transaction, but it got nil\nTry: pass the pool from db.Open")
	}
	switch {
	case !actionPattern.MatchString(e.Action):
		return fmt.Errorf("audit: record an event: the action %q does not match %s\nTry: use lower-case letters, digits, '_', and '.', such as report.render", e.Action, actionPattern)
	case e.Table == "" || e.RecordID == "":
		return fmt.Errorf("audit: record the event %s: the table and the record ID are required\nTry: set Event.Table and Event.RecordID (use \"*\" for the whole table)", e.Action)
	case len(e.Changes) > 0 && !json.Valid(e.Changes):
		return fmt.Errorf("audit: record the event %s: the changes are not valid JSON\nTry: build Event.Changes with json.Marshal", e.Action)
	}
	actor := e.Actor
	if actor.Kind == "" {
		actor, _ = ActorFrom(ctx)
	}
	var changes, owner, actorID any
	if len(e.Changes) > 0 {
		changes = string(e.Changes)
	}
	if e.OwnerID != 0 {
		owner = e.OwnerID
	}
	if actor.ID != 0 {
		actorID = actor.ID
	}
	_, err := q.Exec(ctx, `SELECT audit_append($1::text, $2::text, $3::text, $4::bigint, $5::jsonb, $6::text, $7::bigint,
		$8::text, $9::text, $10::text, $11::text, $12::text)`,
		e.Action, e.Table, e.RecordID, owner, changes, actor.Kind, actorID,
		actor.Email, actor.Role, actor.Source, actor.Reason, actor.RequestID)
	return wrap("record the event "+e.Action, err, "check the event fields and the database connection, then retry")
}

// List returns the events that match f, newest first. It returns an error that
// wraps ErrFilterLimit when f.Validate fails.
func List(ctx context.Context, q Querier, f Filter) ([]Event, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if q == nil {
		return nil, errors.New("audit: List needs a database pool or a transaction, but it got nil\nTry: pass the pool from db.Open")
	}
	limit := f.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	return list(ctx, q, f, limit)
}

// eventColumns are the columns that scanEvent reads, in order.
const eventColumns = `id, occurred_at, action, table_name, record_id, owner_id, changes::text,
	actor_kind, actor_id, actor_email, actor_role, source, reason, request_id`

// list runs the query of f with limit, newest first. It does not check the
// limit: List checks it for the API, and the command has its own maximum.
func list(ctx context.Context, q Querier, f Filter, limit int) ([]Event, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.Table != "" {
		add("table_name = $%d", f.Table)
	}
	if f.RecordID != "" {
		add("record_id = $%d", f.RecordID)
	}
	if f.OwnerID != 0 {
		add("owner_id = $%d", f.OwnerID)
	}
	if f.ActorEmail != "" {
		add("actor_email = $%d", f.ActorEmail)
	}
	if f.Action != "" {
		add("action = $%d", f.Action)
	}
	if !f.Since.IsZero() {
		add("occurred_at >= $%d", f.Since)
	}
	if !f.Until.IsZero() {
		add("occurred_at < $%d", f.Until)
	}
	if f.Unattributed {
		where = append(where, "actor_kind = 'unattributed'")
	}
	if f.BeforeID != 0 {
		add("id < $%d", f.BeforeID)
	}
	sql := `SELECT ` + eventColumns + ` FROM audit_events`
	if len(where) > 0 {
		sql += ` WHERE ` + strings.Join(where, ` AND `)
	}
	args = append(args, limit, f.Offset)
	sql += fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, wrap("list the events", err, "check the database connection, then retry")
	}
	events, err := pgx.CollectRows(rows, scanEvent)
	return events, wrap("read the events", err, "check the database connection, then retry")
}

// scanEvent reads one row of eventColumns.
func scanEvent(row pgx.CollectableRow) (Event, error) {
	var e Event
	var owner, actorID *int64
	var email, role, reason, requestID *string
	var changes string
	err := row.Scan(&e.ID, &e.OccurredAt, &e.Action, &e.Table, &e.RecordID, &owner, &changes,
		&e.Actor.Kind, &actorID, &email, &role, &e.Actor.Source, &reason, &requestID)
	if err != nil {
		return Event{}, err
	}
	e.OccurredAt = e.OccurredAt.UTC()
	e.Changes = json.RawMessage(changes)
	e.OwnerID = deref(owner)
	e.Actor.ID = deref(actorID)
	e.Actor.Email = deref(email)
	e.Actor.Role = deref(role)
	e.Actor.Reason = deref(reason)
	e.Actor.RequestID = deref(requestID)
	return e, nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// Prune deletes the events before the time before through audit_prune and
// returns the count. It refuses an age below 1 day and needs a reason. It binds
// the actor of ctx, so the audit.prune event names who pruned.
func Prune(ctx context.Context, pool *pgxpool.Pool, before time.Time, reason string) (int64, error) {
	if strings.TrimSpace(reason) == "" {
		return 0, errors.New("audit: prune: a reason is required\nTry: pass a reason, for example the retention rule")
	}
	if pool == nil {
		return 0, errors.New("audit: Prune needs a database pool, but it got nil\nTry: pass the pool from db.Open")
	}
	if before.IsZero() {
		return 0, errors.New("audit: prune: the time is required\nTry: pass a time at least 1 day in the past")
	}
	var removed int64
	err := InTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT audit_prune($1, $2)`, before, reason).Scan(&removed)
	})
	if err != nil {
		var de *dbError
		if errors.As(err, &de) {
			return 0, err
		}
		return 0, wrap("prune", err, "pass a time at least 1 day in the past, then retry")
	}
	return removed, nil
}
