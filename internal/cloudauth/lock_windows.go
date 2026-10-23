//go:build windows

package cloudauth

import (
	"context"
	"errors"
	"os"
	"time"
)

// staleLock is the age after which a lock file counts as left by a dead process.
const staleLock = 2 * time.Minute

func acquire(ctx context.Context, path string) (func(), error) {
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return func() {
				_ = f.Close()
				_ = os.Remove(path)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) && !errors.Is(err, os.ErrPermission) {
			return nil, problem("lock the credentials", err.Error(), "")
		}
		if fi, serr := os.Stat(path); serr == nil && time.Since(fi.ModTime()) > staleLock {
			_ = os.Remove(path)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, problem("lock the credentials", "another spinup process holds the lock: "+ctx.Err().Error(), "wait for the other process, then try again")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
