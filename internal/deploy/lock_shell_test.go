package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// localVM runs the exact remote shell scripts against an isolated local tree.
type localVM struct{ root string }

func (v localVM) path(s string) string { return strings.ReplaceAll(s, "/opt/app", v.root) }
func (v localVM) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	argv := append([]string(nil), c.Argv...)
	for i := range argv {
		argv[i] = v.path(argv[i])
	}
	return (runexec.OSRunner{}).Run(ctx, runexec.Cmd{Name: argv[0], Args: argv[1:], Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr, Redact: c.Redact, Timeout: c.Timeout})
}
func (v localVM) Upload(_ context.Context, data []byte, path string, mode fs.FileMode) error {
	return os.WriteFile(v.path(path), data, mode)
}
func (localVM) Close(context.Context) error { return nil }

func TestLockShellOwnershipAndTTL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vm := localVM{root: t.TempDir()}
	now := fixedTime
	opts := LockOptions{Owner: "alice ' $(false)", Release: "r1", TTL: time.Minute, Now: func() time.Time { return now }, Warn: io.Discard}
	first, err := AcquireLock(ctx, vm, "app", opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AcquireLock(ctx, vm, "app", opts)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("held error = %v", err)
	}
	now = now.Add(30 * time.Second)
	if err := first.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Second)
	_, err = AcquireLock(ctx, vm, "app", opts)
	if !errors.As(err, &locked) {
		t.Fatalf("refreshed lock expired too soon: %v", err)
	}
	now = now.Add(30 * time.Second) // The TTL expires exactly at this instant.
	opts.Release = "r2"
	second, err := AcquireLock(ctx, vm, "app", opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Release(ctx); err == nil {
		t.Fatal("stale release removed the replacement lock")
	}
	if err := first.Refresh(ctx); err == nil {
		t.Fatal("stale refresh replaced the replacement owner")
	}
	info, err := ReadLock(ctx, vm, "app")
	if err != nil || info.Release != "r2" {
		t.Fatalf("replacement lock = %#v, %v", info, err)
	}
	files, err := filepath.Glob(filepath.Join(vm.root, "deploy.lock.refresh-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("refresh temporary files = %v, %v", files, err)
	}
	if err := second.Release(ctx); err != nil {
		t.Fatal(err)
	}
	info, err = ReadLock(ctx, vm, "app")
	if info != nil || err != nil {
		t.Fatalf("released lock = %#v, %v", info, err)
	}
	if _, err := AcquireLock(ctx, vm, "app", opts); err != nil {
		t.Fatal(err)
	}
	if err := ForceUnlock(ctx, vm, "app"); err != nil {
		t.Fatal(err)
	}
	info, err = ReadLock(ctx, vm, "app")
	if info != nil || err != nil {
		t.Fatalf("forced unlock = %#v, %v", info, err)
	}
}

func TestLockShellContention(t *testing.T) {
	t.Parallel()
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "expired"}[expired], func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			vm := localVM{root: t.TempDir()}
			now := fixedTime
			if expired {
				if _, err := AcquireLock(ctx, vm, "app", LockOptions{Owner: "old", TTL: time.Second, Now: func() time.Time { return now.Add(-time.Minute) }}); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			acquired := make(chan *Lock, 8)
			for i := range 8 {
				wg.Go(func() {
					lock, err := AcquireLock(ctx, vm, "app", LockOptions{Owner: "contender", Release: time.Duration(i).String(), Now: func() time.Time { return now }, Warn: io.Discard})
					if err == nil {
						acquired <- lock
					}
				})
			}
			wg.Wait()
			close(acquired)
			var winners []*Lock
			for lock := range acquired {
				winners = append(winners, lock)
			}
			if len(winners) != 1 {
				t.Fatalf("acquired %d locks, want one", len(winners))
			}
			if err := winners[0].Release(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLockShellExpiredFormattedMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vm := localVM{root: t.TempDir()}
	opts := LockOptions{Owner: "old", TTL: time.Second, Now: func() time.Time { return fixedTime.Add(-time.Minute) }}
	old, err := AcquireLock(ctx, vm, "app", opts)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(old.Info, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vm.root, "deploy.lock", "owner.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	opts.Owner = "new"
	opts.Now = func() time.Time { return fixedTime }
	opts.Warn = io.Discard
	replacement, err := AcquireLock(ctx, vm, "app", opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Release(ctx); err != nil {
		t.Fatal(err)
	}
}
