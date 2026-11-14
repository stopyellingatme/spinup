package live

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// publishSQL is the statement of ADR 0033 section 5.3, with RETURNING seq for
// the span. The update of the feed row locks it until the transaction ends,
// so the sequence has no gap and follows the commit order.
const publishSQL = `WITH f AS (
    INSERT INTO live_feeds AS f (key, seq) VALUES ($1, 1)
    ON CONFLICT (key) DO UPDATE SET seq = f.seq + 1, updated_at = now()
    RETURNING seq
)
INSERT INTO live_outbox (feed, seq, changes) SELECT $1, seq, $2::jsonb FROM f
RETURNING seq`

// The statements of ADR 0033 section 5.5. Each one deletes at most pruneBatch rows.
const (
	prunePatchesSQL = `DELETE FROM live_outbox WHERE (feed, seq) IN
    (SELECT feed, seq FROM live_outbox WHERE created_at < now() - $1::interval ORDER BY created_at LIMIT 10000)`
	// The outer conditions repeat the inner ones. When a concurrent Publish
	// updates a feed row, PostgreSQL checks the outer conditions again on the
	// new row version, and the recent updated_at keeps the feed.
	pruneFeedsSQL = `DELETE FROM live_feeds f WHERE f.key IN
    (SELECT g.key FROM live_feeds g WHERE g.updated_at < now() - $1::interval
        AND NOT EXISTS (SELECT 1 FROM live_outbox o WHERE o.feed = g.key) LIMIT 10000)
    AND f.updated_at < now() - $1::interval
    AND NOT EXISTS (SELECT 1 FROM live_outbox o WHERE o.feed = f.key)`
	pruneBatch = 10000
)

// migrateTry is the Try line for an undefined table (ADR 0033 section 15.2).
const migrateTry = "run the migrations (spinup db migrate up) with the live module on (spinup module add live)"

// Publish writes one patch to the feed in the transaction tx. The patch holds
// all the changes, with one sequence number. If tx rolls back, nothing is
// published. Publish validates the key and the changes before it runs SQL. Call
// it last in the transaction, because it locks the row of the feed until the
// transaction ends.
func Publish(ctx context.Context, tx pgx.Tx, key Key, changes ...Change) error {
	if err := key.Valid(); err != nil {
		return err
	}
	if tx == nil {
		return &Error{Op: "publish", Err: errors.New("the transaction is nil"), Try: "call Publish inside the transaction of the write, such as in db.InTx"}
	}
	patch, err := marshalChanges(changes)
	if err != nil {
		return err
	}
	ctx, span := otel.GetTracerProvider().Tracer(instrumentationName).Start(ctx, "live.publish",
		trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(attribute.String("live.feed", key.Feed())))
	defer span.End()
	var seq int64
	if err := tx.QueryRow(ctx, publishSQL, string(key), string(patch)).Scan(&seq); err != nil {
		err = dbError("publish", err)
		failSpan(span, err)
		return err
	}
	span.SetAttributes(attribute.Int64("live.seq", seq))
	return nil
}

// PruneOptions sets the ages that Prune uses.
type PruneOptions struct {
	Patches time.Duration // delete patches older than this; 0 means 1 hour
	Feeds   time.Duration // delete feeds with no patch and no update for this long; 0 means 30 days
}

// Pruned holds the counts of the rows that Prune deleted.
type Pruned struct{ Patches, Feeds int64 }

// Prune deletes old patches and idle feeds, in batches of 10,000 rows. It is
// safe to run on many instances at the same time. A feed that a concurrent
// Publish updates stays.
func Prune(ctx context.Context, pool *pgxpool.Pool, o PruneOptions) (Pruned, error) {
	var out Pruned
	if pool == nil {
		return out, &Error{Op: "prune", Err: errors.New("the pool is nil"), Try: "pass the pool of the app, such as the pool from db.Open"}
	}
	if o.Patches <= 0 {
		o.Patches = defaultRetention
	}
	if o.Feeds <= 0 {
		o.Feeds = pruneFeedAge
	}
	for {
		tag, err := pool.Exec(ctx, prunePatchesSQL, o.Patches)
		if err != nil {
			return out, dbError("prune patches", err)
		}
		out.Patches += tag.RowsAffected()
		if tag.RowsAffected() < pruneBatch {
			break
		}
	}
	for {
		tag, err := pool.Exec(ctx, pruneFeedsSQL, o.Feeds)
		if err != nil {
			return out, dbError("prune feeds", err)
		}
		out.Feeds += tag.RowsAffected()
		if tag.RowsAffected() < pruneBatch {
			break
		}
	}
	return out, nil
}

// dbError wraps a database failure in an *Error with a Try line.
func dbError(op string, err error) error {
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "42P01":
		return &Error{Op: op, Err: err, Try: migrateTry}
	case errors.Is(err, pgx.ErrTxClosed):
		return &Error{Op: op, Err: err, Try: "call Publish before the transaction commits or rolls back"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &Error{Op: op, Err: err, Try: "allow more time, then retry"}
	default:
		return &Error{Op: op, Err: err, Try: "check the database connection, then retry"}
	}
}

// failSpan records a failure on span without row data.
func failSpan(span trace.Span, err error) {
	cause := err
	var le *Error
	if errors.As(err, &le) {
		cause = le.Err
	}
	span.SetStatus(codes.Error, "live operation failed")
	span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", cause)))
}
