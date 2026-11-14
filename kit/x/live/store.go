// Stub of unit LV3: the unit replaces this file.

package live

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Publish writes one patch to the feed in the transaction tx.
func Publish(_ context.Context, _ pgx.Tx, _ Key, _ ...Change) error {
	panic("live: stub of unit LV3")
}

// PruneOptions sets the ages that Prune uses.
type PruneOptions struct {
	Patches time.Duration // delete patches older than this; 0 means 1 hour
	Feeds   time.Duration // delete feeds with no patch and no update for this long; 0 means 30 days
}

// Pruned holds the counts of the rows that Prune deleted.
type Pruned struct{ Patches, Feeds int64 }

// Prune deletes old patches and idle feeds.
func Prune(_ context.Context, _ *pgxpool.Pool, _ PruneOptions) (Pruned, error) {
	panic("live: stub of unit LV3")
}
