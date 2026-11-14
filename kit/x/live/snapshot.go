// Stub of unit LV4: the unit replaces this file.

package live

import (
	"context"
	"net/http"
)

// Snapshot returns the handler of the snapshot route of a feed.
func Snapshot[T any](_ *Hub, _ KeyFunc, _ func(ctx context.Context, key Key, limit int) ([]T, error)) http.Handler {
	panic("live: stub of unit LV4")
}
