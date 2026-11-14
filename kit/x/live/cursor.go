// Stub of unit LV2: the unit replaces this file.

package live

// Cursor is a position in a feed. The epoch is a time in milliseconds since
// 1970. The value 0-0 means "the feed has no row yet".
type Cursor struct{ Epoch, Seq int64 }

// String returns the form "<epoch>-<seq>", for example "1731500000000-42".
func (c Cursor) String() string { panic("live: stub of unit LV2") }

// ParseCursor parses two decimal numbers of 1 to 18 digits with one "-" between them.
func ParseCursor(_ string) (Cursor, error) { panic("live: stub of unit LV2") }
