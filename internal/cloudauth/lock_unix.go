//go:build unix

package cloudauth

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

func acquire(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, problem("lock the credentials", err.Error(), "check the permissions of the directory of "+path)
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, problem("lock the credentials", err.Error(), "")
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, problem("lock the credentials", "another spinup process holds the lock: "+ctx.Err().Error(), "wait for the other process, then try again")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
