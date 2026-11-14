// Package live serves live data feeds over server-sent events (ADR 0033).
// A feed has a snapshot, numbered patches, and resume by cursor. PostgreSQL is
// the only broker: the tables live_feeds and live_outbox hold the position and
// the patches of each feed, and NOTIFY tells each API instance about a change.
//
// [Publish] writes one patch to a feed in the transaction of the write. If the
// transaction rolls back, nothing is published.
//
// A [Hub] holds the streams of one process. It listens for notifications, polls
// as a fallback, and keeps a ring of encoded frames for each feed. It holds no rows.
//
// [Hub.Stream] returns the handler of the stream route of a feed. A client
// resumes with the cursor of its last patch. When the history is not available,
// the stream sends a sync event, and the client loads the snapshot again.
//
// [Snapshot] returns the handler of the snapshot route of a feed. It reads the
// cursor first and then the rows, so the patches after the cursor replay in order.
//
// The package is under kit/x, so its API can change before v1.
package live
