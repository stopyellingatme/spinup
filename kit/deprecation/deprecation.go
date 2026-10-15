// Package deprecation reports use of deprecated kit paths at run time.
//
// A deprecated function calls Warn on its first use. Warn logs one warning
// for each key in each process, so a hot path does not flood the log. The
// policy is in docs/policy/kit-compat.md.
package deprecation

import (
	"log/slog"
	"sync"
)

var seen sync.Map

// Warn logs message at warning level the first time it sees key. Later calls
// with the same key do nothing. If logger is nil, Warn uses slog.Default.
// Warn is safe for concurrent use. It reports whether it logged the message.
func Warn(logger *slog.Logger, key, message string) bool {
	if _, loaded := seen.LoadOrStore(key, struct{}{}); loaded {
		return false
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn(message, slog.String("deprecated", key))
	return true
}
