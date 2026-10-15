package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// LockInfo is the deploy.lock/owner.json record described by ADR 0009.
type LockInfo struct {
	Owner      string    `json:"owner"`
	PID        int       `json:"pid"`
	Host       string    `json:"host"`
	Acquired   time.Time `json:"acquired"`
	TTLSeconds int64     `json:"ttl_seconds"`
	Release    string    `json:"release"`
}

// LockOptions supplies lock ownership, lifetime, clock, and warning output.
type LockOptions struct {
	Owner, Release string
	// TTL defaults to fifteen minutes. Positive fractional seconds round up.
	TTL time.Duration
	// Now defaults to time.Now. File timestamps are UTC with second precision.
	Now func() time.Time
	// Warn receives takeover warnings. A nil writer uses os.Stderr.
	Warn io.Writer
}

// LockedError reports a lock held by another deployment.
// Its wrapped ui.Error supplies the CLI exit code and recovery guidance.
type LockedError struct{ Info LockInfo }

// Error returns the lock owner and recovery guidance.
func (e *LockedError) Error() string { u := e.userError(); return u.Error() + "\nTry: " + u.Try }

// Unwrap exposes the user-facing error with ui.ExitLocked.
func (e *LockedError) Unwrap() error { return e.userError() }

func (e *LockedError) userError() *ui.Error {
	return &ui.Error{What: "deploy lock is held", Why: fmt.Sprintf("owner %q, pid %d, host %q, release %q, acquired %s", e.Info.Owner, e.Info.PID, e.Info.Host, e.Info.Release, e.Info.Acquired.UTC().Format(time.RFC3339)), Code: ui.ExitLocked, Try: "wait, or run spinup deploy unlock --force"}
}

// Lock is a deploy lock handle. Refresh extends its TTL from the injected clock.
// Release and Refresh reject a changed owner record, including an expired takeover.
type Lock struct {
	Info     LockInfo
	remote   remote.Runner
	path     string
	now      func() time.Time
	mu       sync.Mutex
	released bool
}

var deployAppPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func appRoot(app string) (string, error) {
	if !deployAppPattern.MatchString(app) {
		return "", fmt.Errorf("invalid deploy app name %q\nTry: use lowercase letters, digits, and hyphens, starting with a letter", app)
	}
	return "/opt/" + app, nil
}

func utcNow(now func() time.Time) time.Time {
	if now == nil {
		now = time.Now
	}
	return now().UTC().Truncate(time.Second)
}

// AcquireLock atomically creates a lock, or takes over an expired lock with a warning.
// A missing or invalid owner record fails closed and requires manual inspection.
func AcquireLock(ctx context.Context, r remote.Runner, app string, o LockOptions) (*Lock, error) {
	root, err := appRoot(app)
	if err != nil {
		return nil, err
	}
	if o.TTL < 0 {
		return nil, fmt.Errorf("deploy lock TTL is negative\nTry: supply a positive TTL")
	}
	if o.TTL == 0 {
		o.TTL = 15 * time.Minute
	}
	seconds := int64(o.TTL / time.Second)
	if o.TTL%time.Second != 0 {
		seconds++
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("read lock hostname: %w\nTry: check the local hostname", err)
	}
	if o.Owner == "" {
		o.Owner = os.Getenv("USER")
		if o.Owner == "" {
			o.Owner = host
		}
	}
	l := &Lock{remote: r, path: root + "/deploy.lock", now: o.Now, Info: LockInfo{Owner: o.Owner, PID: os.Getpid(), Host: host, Acquired: utcNow(o.Now), TTLSeconds: seconds, Release: o.Release}}
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"mkdir", l.path}})
	if err != nil {
		if result.ExitCode != 1 {
			return nil, err
		}
		held, expected, readErr := readLockRecord(ctx, r, app)
		if readErr != nil {
			return nil, readErr
		}
		if held == nil {
			return nil, &LockedError{}
		}
		if utcNow(o.Now).Before(held.Acquired.Add(time.Duration(held.TTLSeconds) * time.Second)) {
			return nil, &LockedError{Info: *held}
		}
		// The guard serializes takeover, refresh, and release. A new owner must still win mkdir.
		result, err = r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", guarded(l.path, expected, "rm -rf -- "+remote.Quote(l.path)+" && mkdir "+remote.Quote(l.path))}})
		if err != nil {
			if result.ExitCode == 4 {
				return nil, &LockedError{Info: *held}
			}
			return nil, err
		}
		warning := o.Warn
		if warning == nil {
			warning = os.Stderr
		}
		_, _ = fmt.Fprintf(warning, "Warning: taking over expired deploy lock held by %q for release %q\n", held.Owner, held.Release)
	}
	data, err := json.Marshal(l.Info)
	if err != nil {
		return nil, err
	}
	if err := r.Upload(ctx, data, l.path+"/owner.json", 0600); err != nil {
		// Leave the incomplete lock for explicit recovery. Its state may be ambiguous after SSH loss.
		return nil, fmt.Errorf("write deploy lock owner: %w\nTry: inspect the lock, or run spinup deploy unlock --force", err)
	}
	return l, nil
}

// ReadLock returns nil when no lock directory exists. Invalid metadata returns an error.
func ReadLock(ctx context.Context, r remote.Runner, app string) (*LockInfo, error) {
	info, _, err := readLockRecord(ctx, r, app)
	return info, err
}

func readLockRecord(ctx context.Context, r remote.Runner, app string) (*LockInfo, []byte, error) {
	root, err := appRoot(app)
	if err != nil {
		return nil, nil, err
	}
	path := root + "/deploy.lock"
	script := "if [ -d " + remote.Quote(path) + " ]; then cat " + remote.Quote(path+"/owner.json") + "; elif [ -e " + remote.Quote(path) + " ]; then exit 1; else exit 3; fi"
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", script}})
	if err != nil {
		if result.ExitCode == 3 {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read deploy lock: %w\nTry: inspect the lock, or run spinup deploy unlock --force", err)
	}
	var info LockInfo
	if err := json.Unmarshal(result.Stdout, &info); err != nil {
		return nil, nil, fmt.Errorf("decode deploy lock: %w\nTry: inspect owner.json before forcing an unlock", err)
	}
	if info.Acquired.IsZero() || info.TTLSeconds <= 0 || info.TTLSeconds > int64((1<<63-1)/time.Second) {
		return nil, nil, fmt.Errorf("deploy lock metadata has an invalid timestamp or TTL\nTry: inspect owner.json before forcing an unlock")
	}
	return &info, []byte(strings.TrimRight(string(result.Stdout), "\n")), nil
}

// Release removes this handle's lock. Repeated successful releases do nothing.
func (l *Lock) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	expected, err := json.Marshal(l.Info)
	if err != nil {
		return err
	}
	_, err = l.remote.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", guarded(l.path, expected, "rm -rf -- "+remote.Quote(l.path))}})
	if err != nil {
		return fmt.Errorf("release deploy lock: %w\nTry: inspect the current lock owner before forcing an unlock", err)
	}
	l.released = true
	return nil
}

func (l *Lock) refreshPath() string {
	data, _ := json.Marshal(l.Info)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%s.refresh-%x", l.path, sum[:12])
}

// Refresh extends this handle's lock without changing another deployment's owner record.
func (l *Lock) Refresh(ctx context.Context) error {
	return l.refresh(ctx, "")
}

// refresh can also name the selected redeploy directory in the owner record.
func (l *Lock) refresh(ctx context.Context, release string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return fmt.Errorf("cannot refresh a released deploy lock\nTry: acquire a new lock")
	}
	expected, err := json.Marshal(l.Info)
	if err != nil {
		return err
	}
	updated := l.Info
	updated.Acquired = utcNow(l.now)
	if release != "" {
		updated.Release = release
	}
	data, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	tmp := l.refreshPath()
	if err := l.remote.Upload(ctx, data, tmp, 0600); err != nil {
		return err
	}
	// Remove the staged file even when ownership changes or the guard is busy.
	script := guardedWithCleanup(l.path, expected, "mv -f -- "+remote.Quote(tmp)+" "+remote.Quote(l.path+"/owner.json"), "rm -f -- "+remote.Quote(tmp))
	_, err = l.remote.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", script}})
	if err != nil {
		return fmt.Errorf("refresh deploy lock: %w\nTry: inspect the current lock owner", err)
	}
	l.Info = updated
	return nil
}

// ForceUnlock removes a lock and its mutation guard without checking ownership.
func ForceUnlock(ctx context.Context, r remote.Runner, app string) error {
	root, err := appRoot(app)
	if err != nil {
		return err
	}
	_, err = r.Run(ctx, remote.Cmd{Argv: []string{"rm", "-rf", "--", root + "/deploy.lock", root + "/deploy.lock.guard"}})
	return err
}

func guarded(path string, expected []byte, action string) string {
	return guardedWithCleanup(path, expected, action, "")
}

func guardedWithCleanup(path string, expected []byte, action, cleanup string) string {
	guard := path + ".guard"
	prefix := ""
	if cleanup != "" {
		prefix = "trap " + remote.Quote(cleanup) + " 0; "
		cleanup += "; "
	}
	cleanup += "rmdir -- " + remote.Quote(guard)
	return prefix + "mkdir " + remote.Quote(guard) + " || exit 4; trap " + remote.Quote(cleanup) + " 0; trap 'exit 1' HUP INT TERM; [ \"$(cat " + remote.Quote(path+"/owner.json") + ")\" = " + remote.Quote(string(expected)) + " ] || exit 4; " + action
}
