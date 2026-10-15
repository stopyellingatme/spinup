package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var noticeMu sync.Mutex

const staleNoticeLockAge = time.Minute

// NoticeOptions controls the silent daily release check.
type NoticeOptions struct {
	Current  string
	TTY      bool
	JSON     bool
	Offline  bool
	CI       bool
	Disabled bool
	CacheDir string
	Now      time.Time
}

// Notice checks at most once per day, with a two-second deadline.
// It returns an empty string on any failure or when a check is disabled.
func (u Updater) Notice(ctx context.Context, o NoticeOptions) string {
	if !o.TTY || o.JSON || o.Offline || o.CI || o.Disabled {
		return ""
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	cache := o.CacheDir
	if cache == "" {
		var err error
		cache, err = os.UserCacheDir()
		if err != nil {
			return ""
		}
	}
	noticeMu.Lock()
	defer noticeMu.Unlock()
	dir := filepath.Join(cache, "spinup")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ""
	}
	// Coordinate checks from concurrent CLI processes as well as goroutines.
	lock := filepath.Join(dir, "update-check.lock")
	if err := acquireNoticeLock(lock, now); err != nil {
		return ""
	}
	defer os.Remove(lock)
	stamp := filepath.Join(dir, "update-check")
	if data, err := os.ReadFile(stamp); err == nil {
		if last, e := time.Parse(time.RFC3339Nano, string(data)); e == nil && now.Sub(last) < 24*time.Hour {
			return ""
		}
	}
	// Claim the interval before the request, including failed requests.
	if err := os.WriteFile(stamp, []byte(now.Format(time.RFC3339Nano)), 0600); err != nil {
		return ""
	}
	timeout, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r, err := u.Check(timeout, Options{Current: o.Current})
	if err != nil || !r.Available {
		return ""
	}
	return "spinup " + r.Version + " is available. Run spinup self-update."
}

func acquireNoticeLock(lock string, now time.Time) error {
	if err := os.Mkdir(lock, 0700); err == nil || !os.IsExist(err) {
		return err
	}
	info, err := os.Stat(lock)
	if err != nil || now.Sub(info.ModTime()) <= staleNoticeLockAge {
		return err
	}
	if err := os.Remove(lock); err != nil {
		return err
	}
	return os.Mkdir(lock, 0700)
}
