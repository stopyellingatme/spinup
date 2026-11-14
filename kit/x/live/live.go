// Stub of unit LV2: the unit replaces this file.

package live

import (
	"errors"
	"net/http"
	"strconv"
)

// Key names one feed. A shared feed has the form "notes". An owner-scoped feed
// has the form "notes:42" (the name, a colon, and the user ID).
// The pattern is ^[a-z][a-z0-9_]{0,47}(:[0-9]{1,18})?$.
type Key string

// SharedKey returns the key of a feed that all permitted users read. It does not validate.
func SharedKey(feed string) Key { return Key(feed) }

// OwnedKey returns the key of the feed of one user. It does not validate.
func OwnedKey(feed string, userID int64) Key {
	return Key(feed + ":" + strconv.FormatInt(userID, 10))
}

// Valid returns an error that names the pattern when k is not a valid key.
func (k Key) Valid() error { panic("live: stub of unit LV2") }

// Feed returns the name without the scope, such as "notes".
func (k Key) Feed() string { panic("live: stub of unit LV2") }

// Op is the kind of a change.
type Op string

// The kinds of a change.
const (
	OpUpsert Op = "upsert" // replace the row with this ID, or add it
	OpDelete Op = "delete" // remove the row with this ID
	OpTouch  Op = "touch"  // the row with this ID changed: load it again
)

// Change is one change of a patch. ID matches ^[A-Za-z0-9_.:-]{1,64}$.
// Row is encoded with encoding/json when the change is published. A delete and a touch carry no row.
type Change struct {
	Op  Op
	ID  string
	Row any
}

// Upsert returns a change that replaces the row with this ID, or adds it.
func Upsert(id string, row any) Change { return Change{Op: OpUpsert, ID: id, Row: row} }

// Delete returns a change that removes the row with this ID.
func Delete(id string) Change { return Change{Op: OpDelete, ID: id} }

// Touch returns a change that tells the client to load the row with this ID again.
func Touch(id string) Change { return Change{Op: OpTouch, ID: id} }

// KeyFunc authorizes a request and names its feed. It returns ErrUnauthorized or
// ErrForbidden to refuse the request. Any other error is a server error.
type KeyFunc func(r *http.Request) (Key, error)

// The errors that a KeyFunc returns to refuse a request.
var (
	ErrUnauthorized = errors.New("live: a signed-in user is required")
	ErrForbidden    = errors.New("live: the user cannot read this feed")
)

// IsStream reports whether r is a stream request: GET, a path that starts with
// "/api/live/", and a path that ends with "/live". Pass it to app.WithRequestTimeoutExempt.
func IsStream(_ *http.Request) bool { panic("live: stub of unit LV2") }
