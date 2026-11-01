// Package audit writes and reads the audit trail of a spinup app (ADR 0025).
//
// The audit module migration (api/db/migrations/00006_audit_events.sql) creates
// the table audit_events and the trigger functions. A trigger on each audited
// table writes one event for each insert, update, delete, and truncate. The
// trigger reads the actor from the transaction-local setting spinup.audit.
//
// Bind an actor to a transaction with [InTx]:
//
//	ctx = audit.WithActor(ctx, actor) // a middleware does this for a signed-in user
//	err := audit.InTx(ctx, pool, func(tx pgx.Tx) error {
//		_, err := queries.WithTx(tx).UpdateWorkOrder(ctx, params)
//		return err
//	})
//
// A write outside [InTx] still writes an event, with the actor kind
// "unattributed". [Record] writes an event that is not a row change, such as a
// sign-in. [List] reads the events of one record with bounded pages, and
// [Prune] is the one delete path.
//
// The package does not import kit/auth. The subpackage authsink connects the
// two.
package audit
