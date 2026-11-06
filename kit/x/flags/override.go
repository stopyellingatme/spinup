package flags

import (
	"sync"
	"sync/atomic"
)

// The override table of flagstest.Override (ADR 0029 decision 4.8).
// overrideActive is the fast path: when it is false, an evaluation does
// not take the lock.
var (
	overrideActive atomic.Bool
	overrideMu     sync.RWMutex
	overrides      = map[string]bool{}
)

// SetOverride forces key to value for every evaluation in this process.
// It returns a function that restores the earlier state of key. Tests
// use flagstest.Override, which calls the restore function at cleanup.
// An overridden evaluation has the reason static. An undeclared key works.
func SetOverride(key string, value bool) (restore func()) {
	overrideMu.Lock()
	defer overrideMu.Unlock()
	previous, had := overrides[key]
	overrides[key] = value
	overrideActive.Store(true)
	var once sync.Once
	return func() {
		once.Do(func() {
			overrideMu.Lock()
			defer overrideMu.Unlock()
			if had {
				overrides[key] = previous
			} else {
				delete(overrides, key)
			}
			overrideActive.Store(len(overrides) > 0)
		})
	}
}

// lookupOverride returns the overridden resolution of key. ok is false
// when no override is active for key.
func lookupOverride(key string) (r Resolution, ok bool) {
	if !overrideActive.Load() {
		return Resolution{}, false
	}
	overrideMu.RLock()
	value, ok := overrides[key]
	overrideMu.RUnlock()
	if !ok {
		return Resolution{}, false
	}
	return resolution(value, ReasonStatic), true
}
