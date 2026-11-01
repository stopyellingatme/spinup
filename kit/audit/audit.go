package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// errNotImplemented is the result of the contract stubs. Unit K replaces them.
var errNotImplemented = errors.New("audit: not implemented")

// Execer runs a statement. A *pgxpool.Pool, a *pgx.Conn, and a pgx.Tx satisfy it.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Querier runs a query. A *pgxpool.Pool, a *pgx.Conn, and a pgx.Tx satisfy it.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// The actor kinds. The table audit_events accepts only these values.
const (
	// KindUser is a signed-in user of the app.
	KindUser = "user"
	// KindOperator is a person who runs a command of the app binary.
	KindOperator = "operator"
	// KindSystem is the app itself: a job, a migration, or a registration.
	KindSystem = "system"
	// KindUnattributed is a write with no bound actor.
	KindUnattributed = "unattributed"
)

// The sources of an event.
const (
	// SourceAPI is a request to the API.
	SourceAPI = "api"
	// SourceCLI is a command of the app binary.
	SourceCLI = "cli"
	// SourceJob is a background job.
	SourceJob = "job"
	// SourceMigrate is a data migration.
	SourceMigrate = "migrate"
	// SourceSQL is a write with no bound actor, such as psql.
	SourceSQL = "sql"
)

// Actor is who made a change. Its JSON form is the value of the setting
// spinup.audit. Every field is optional. A zero ID means no user ID.
type Actor struct {
	// Kind is KindUser, KindOperator, KindSystem, or KindUnattributed.
	Kind string `json:"kind,omitempty"`
	// ID is the users.id of the actor, or 0.
	ID int64 `json:"id,omitempty"`
	// Email is the email of the actor at the time of the event.
	Email string `json:"email,omitempty"`
	// Role is the role of the actor at the time of the event.
	Role string `json:"role,omitempty"`
	// Source is SourceAPI, SourceCLI, SourceJob, SourceMigrate, or SourceSQL.
	Source string `json:"source,omitempty"`
	// Reason is free text from the operator or the code.
	Reason string `json:"reason,omitempty"`
	// RequestID is the ID of the HTTP request, if there is one.
	RequestID string `json:"request_id,omitempty"`
}

// Event is one row of audit_events.
type Event struct {
	// ID is the order key. Record ignores it.
	ID int64 `json:"id"`
	// OccurredAt is the database clock at the time of the event. Record ignores it.
	OccurredAt time.Time `json:"occurred_at"`
	// Action is create, update, delete, truncate, or an event name such as
	// auth.sign_in. It matches ^[a-z][a-z0-9_.]{0,63}$.
	Action string `json:"action"`
	// Table is the subject table. Auth events use users.
	Table string `json:"table_name"`
	// RecordID is the key of the subject row. Composite keys are joined with ":".
	// The value "*" means the whole table.
	RecordID string `json:"record_id"`
	// OwnerID is the owner column value of the row, or 0 for none.
	OwnerID int64 `json:"owner_id,omitempty"`
	// Changes holds the field changes: {"col": {"old": v, "new": v}}.
	Changes json.RawMessage `json:"changes"`
	// Actor is who made the change. For Record, a zero Kind means the actor of the context.
	Actor Actor `json:"actor"`
}

// Page limits of List. The HTTP history endpoint uses the same values.
const (
	// DefaultLimit is the page size when Filter.Limit is 0.
	DefaultLimit = 50
	// MaxLimit is the largest Filter.Limit.
	MaxLimit = 200
	// MaxOffset is the largest Filter.Offset.
	MaxOffset = 10000
)

// ErrFilterLimit is the error of Filter.Validate for a limit or an offset that is
// out of range.
var ErrFilterLimit = errors.New("audit: the page is out of range")

// Filter selects events for List. Empty fields do not filter.
type Filter struct {
	// Table is the subject table.
	Table string
	// RecordID is the key of the subject row.
	RecordID string
	// OwnerID keeps only the events with this owner_id. 0 means no owner filter.
	OwnerID int64
	// ActorEmail keeps only the events of this actor email.
	ActorEmail string
	// Action keeps only the events with this action.
	Action string
	// Since keeps the events at or after this time.
	Since time.Time
	// Until keeps the events before this time.
	Until time.Time
	// Unattributed keeps only the events with no actor.
	Unattributed bool
	// BeforeID is the cursor: it keeps the events with an ID below it. 0 means the newest.
	BeforeID int64
	// Limit is the page size. 0 means DefaultLimit. The maximum is MaxLimit.
	Limit int
	// Offset skips events. The maximum is MaxOffset.
	Offset int
}

// Validate checks the limit and the offset. The error wraps ErrFilterLimit and
// ends with a Try: line.
func (f Filter) Validate() error {
	switch {
	case f.Limit < 0 || f.Limit > MaxLimit:
		return fmt.Errorf("%w: limit %d is not from 0 to %d\nTry: use a limit of at most %d and the cursor for more events", ErrFilterLimit, f.Limit, MaxLimit, MaxLimit)
	case f.Offset < 0 || f.Offset > MaxOffset:
		return fmt.Errorf("%w: offset %d is not from 0 to %d\nTry: use the cursor or a narrower time range", ErrFilterLimit, f.Offset, MaxOffset)
	}
	return nil
}

// ErrNotInTx is the error of Bind when its argument is a pool or a connection. A
// transaction-local setting is lost outside a transaction.
var ErrNotInTx = errors.New("audit: Bind needs a transaction, not a pool or a connection\nTry: use audit.InTx, or pass the pgx.Tx of db.InTx")

type actorKey struct{}

// WithActor returns a copy of ctx that holds the actor.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor of ctx and whether ctx holds one.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// Operator returns the actor of a command of the app binary: the kind operator,
// the source cli, and the email operator:<login>. The login is SUDO_USER, then
// USER, then unknown.
func Operator() Actor {
	login := os.Getenv("SUDO_USER")
	if login == "" {
		login = os.Getenv("USER")
	}
	if login == "" {
		login = "unknown"
	}
	return Actor{Kind: KindOperator, Source: SourceCLI, Email: "operator:" + login}
}

// System returns an actor of the kind system, such as System("job", "send_mail").
func System(source, reason string) Actor {
	return Actor{Kind: KindSystem, Source: source, Reason: reason}
}

// Bind sets the actor of ctx as the transaction-local setting spinup.audit, so
// the triggers of the transaction read it. It returns ErrNotInTx when tx is a
// *pgxpool.Pool or a *pgx.Conn.
func Bind(ctx context.Context, tx Execer) error {
	return errNotImplemented
}

// InTx runs fn in a transaction (db.InTx) after Bind with the actor of ctx.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	return errNotImplemented
}

// Record writes an event that is not a row change, such as auth.sign_in. It
// takes the actor from e.Actor, or from ActorFrom(ctx) when e.Actor.Kind is
// empty, and passes it to audit_append. It reads no setting, so q can be a pool.
func Record(ctx context.Context, q Execer, e Event) error {
	return errNotImplemented
}

// List returns the events that match f, newest first. It returns an error that
// wraps ErrFilterLimit when f.Validate fails.
func List(ctx context.Context, q Querier, f Filter) ([]Event, error) {
	return nil, errNotImplemented
}

// Prune deletes the events before the time before through audit_prune and
// returns the count. It refuses an age below 1 day and needs a reason.
func Prune(ctx context.Context, pool *pgxpool.Pool, before time.Time, reason string) (int64, error) {
	return 0, errNotImplemented
}

// Command returns the operator command "audit" of the app binary: list, export,
// and prune. Register it with app.WithCommand("audit", audit.Command(pool)).
func Command(pool *pgxpool.Pool) func(ctx context.Context, args []string) error {
	return func(context.Context, []string) error { return errNotImplemented }
}
