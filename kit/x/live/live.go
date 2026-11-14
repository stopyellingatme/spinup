package live

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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
func (k Key) Valid() error {
	name, scope, scoped := strings.Cut(string(k), ":")
	valid := len(name) >= 1 && len(name) <= 48
	for i := 0; i < len(name); i++ {
		c := name[i]
		valid = valid && (c >= 'a' && c <= 'z' || i > 0 && (c >= '0' && c <= '9' || c == '_'))
	}
	if scoped {
		valid = valid && len(scope) >= 1 && len(scope) <= 18
		for i := 0; i < len(scope); i++ {
			if scope[i] < '0' || scope[i] > '9' {
				valid = false
			}
		}
	}
	if !valid {
		return &Error{Op: "validate key", Err: fmt.Errorf("%q does not match ^[a-z][a-z0-9_]{0,47}(:[0-9]{1,18})?$", k), Try: "use a lowercase feed name and an optional numeric scope"}
	}
	return nil
}

// Feed returns the name without the scope, such as "notes".
func (k Key) Feed() string { return feedOf(k) }

func feedOf(k Key) string {
	name, _, _ := strings.Cut(string(k), ":")
	return name
}

// Op is the kind of a change.
type Op string

const (
	// OpUpsert replaces a row by ID, or adds it.
	OpUpsert Op = "upsert"
	// OpDelete removes a row by ID.
	OpDelete Op = "delete"
	// OpTouch tells the client to load a row again.
	OpTouch Op = "touch"
)

// Change is one change of a patch. ID matches ^[A-Za-z0-9_.:-]{1,64}$.
// Row is encoded with encoding/json when the change is published. A delete and a touch carry no row.
type Change struct {
	// Op selects the kind of change.
	Op Op
	// ID identifies the row.
	ID string
	// Row holds the value for an upsert.
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

var (
	// ErrUnauthorized refuses a request without a signed-in user.
	ErrUnauthorized = errors.New("live: a signed-in user is required")
	// ErrForbidden refuses a request that cannot read the feed.
	ErrForbidden = errors.New("live: the user cannot read this feed")
)

// IsStream reports whether r is a stream request: GET, a path that starts with
// "/api/live/", and a path that ends with "/live". Pass it to app.WithRequestTimeoutExempt.
func IsStream(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/live/") && strings.HasSuffix(r.URL.Path, "/live")
}

const (
	maxChanges    = 100
	maxRowBytes   = 16 << 10
	maxPatchBytes = 64 << 10
	maxIDBytes    = 64
)

// Error describes a failed live operation and a corrective action.
type Error struct {
	// Op names the failed operation.
	Op string
	// Err gives the cause of the failure.
	Err error
	// Try gives the corrective action.
	Try string
}

// Error returns the failure, its cause, and a Try line.
func (e *Error) Error() string { return "live: " + e.Op + ": " + e.Err.Error() + "\nTry: " + e.Try }

// Unwrap returns the cause of the failure.
func (e *Error) Unwrap() error { return e.Err }

func marshalChanges(changes []Change) ([]byte, error) {
	fail := func(why, fix string) ([]byte, error) {
		return nil, &Error{Op: "encode changes", Err: errors.New(why), Try: fix}
	}
	if len(changes) == 0 || len(changes) > maxChanges {
		return fail("a patch requires 1 to 100 changes", "publish between 1 and 100 changes")
	}
	// A struct fixes the member order. RawMessage keeps each row encoded once.
	type wireChange struct {
		Op  Op              `json:"op"`
		ID  string          `json:"id"`
		Row json.RawMessage `json:"row,omitempty"`
	}
	wire := make([]wireChange, len(changes))
	for i, c := range changes {
		if !validID(c.ID) {
			return fail("a change ID does not match ^[A-Za-z0-9_.:-]{1,64}$", "use an ID of 1 to 64 letters, digits, underscores, dots, colons, or dashes")
		}
		wire[i] = wireChange{Op: c.Op, ID: c.ID}
		switch c.Op {
		case OpUpsert:
			if c.Row == nil {
				return fail("an upsert row is nil", "supply a row for each upsert")
			}
			row, err := json.Marshal(c.Row)
			if err != nil {
				return nil, &Error{Op: "encode upsert row", Err: err, Try: "supply a row that encoding/json can encode"}
			}
			if string(row) == "null" {
				return fail("an upsert row is nil", "supply a row for each upsert")
			}
			if len(row) > maxRowBytes {
				return fail("an encoded row exceeds 16384 bytes", "publish live.Touch(id) instead")
			}
			wire[i].Row = row
		case OpDelete, OpTouch:
			if c.Row != nil {
				return fail("a delete or touch carries a row", "omit the row for a delete or touch")
			}
		default:
			return fail("a change has an unknown op", "use live.Upsert, live.Delete, or live.Touch")
		}
	}
	patch, err := json.Marshal(wire)
	if err != nil {
		return nil, &Error{Op: "encode patch", Err: err, Try: "supply changes that encoding/json can encode"}
	}
	if len(patch) > maxPatchBytes {
		return fail("an encoded patch exceeds 65536 bytes", "publish fewer changes or use live.Touch(id)")
	}
	return patch, nil
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > maxIDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ':' || c == '-'
		if !valid {
			return false
		}
	}
	return true
}
