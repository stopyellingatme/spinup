// Stub of unit LV4: the unit replaces this file.

package live

import "context"

// Command returns the app command live (feeds, prune, and reset).
func (h *Hub) Command() func(ctx context.Context, args []string) error {
	panic("live: stub of unit LV4")
}
