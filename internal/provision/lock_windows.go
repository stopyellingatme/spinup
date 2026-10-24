//go:build windows

package provision

import (
	"errors"
	"os"
	"time"
)

// staleLock is the age after which a lock file counts as left by a dead process.
const staleLock = 2 * time.Hour

// tryLock creates path exclusively. It reports false when another process holds the lock.
func tryLock(path string) (func(), bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		if fi, serr := os.Stat(path); serr == nil && time.Since(fi.ModTime()) > staleLock {
			_ = os.Remove(path)
			return tryLock(path)
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return func() {
		_ = f.Close()
		_ = os.Remove(path)
	}, true, nil
}
