package live

import (
	"errors"
	"strconv"
	"strings"
)

// Cursor is a position in a feed. The epoch is a time in milliseconds since
// 1970. The value 0-0 means "the feed has no row yet".
type Cursor struct {
	// Epoch identifies the creation of the feed.
	Epoch int64
	// Seq identifies the patch within the epoch.
	Seq int64
}

// String returns the form "<epoch>-<seq>", for example "1731500000000-42".
func (c Cursor) String() string {
	var buf [41]byte
	b := strconv.AppendInt(buf[:0], c.Epoch, 10)
	b = append(b, '-')
	b = strconv.AppendInt(b, c.Seq, 10)
	return string(b)
}

// ParseCursor parses two decimal numbers of 1 to 18 digits with one "-" between them.
func ParseCursor(s string) (Cursor, error) {
	epoch, seq, ok := strings.Cut(s, "-")
	if ok && cursorDigits(epoch) && cursorDigits(seq) {
		e, eErr := strconv.ParseInt(epoch, 10, 64)
		n, nErr := strconv.ParseInt(seq, 10, 64)
		if eErr == nil && nErr == nil {
			return Cursor{Epoch: e, Seq: n}, nil
		}
	}
	return Cursor{}, &Error{Op: "parse cursor", Err: errors.New("the cursor requires two decimal numbers of 1 to 18 digits separated by one dash"), Try: "pass the cursor of the snapshot, such as 1731500000000-42, or omit since"}
}

func cursorDigits(s string) bool {
	if len(s) == 0 || len(s) > 18 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
