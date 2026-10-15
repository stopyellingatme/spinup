package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

var fixedTime = time.Date(2026, 10, 15, 8, 0, 0, 0, time.FixedZone("test", 3600))

func TestLockAcquireRefreshRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := &remote.Fake{}
	f.Expect("mkdir", "/opt/my-app/deploy.lock").Return("", "", 0)
	f.ExpectUpload("/opt/my-app/deploy.lock/owner.json").Return("", "", 0)
	now := fixedTime
	lock, err := AcquireLock(ctx, f, "my-app", LockOptions{Owner: "alice", Release: "r1", TTL: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	var info LockInfo
	if err := json.Unmarshal(f.Uploads()[0].Data, &info); err != nil {
		t.Fatal(err)
	}
	if info.Owner != "alice" || info.Release != "r1" || info.TTLSeconds != 60 || !info.Acquired.Equal(now) || !strings.Contains(string(f.Uploads()[0].Data), `"acquired":"2026-10-15T07:00:00Z"`) {
		t.Fatalf("owner = %#v, %s", info, f.Uploads()[0].Data)
	}
	now = now.Add(30 * time.Second)
	f.ExpectUpload(lock.refreshPath()).Return("", "", 0)
	f.Expect("sh", "-c").Return("", "", 0)
	if err := lock.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !lock.Info.Acquired.Equal(now) {
		t.Fatalf("refresh = %v", lock.Info.Acquired)
	}
	f.Expect("sh", "-c").Return("", "", 0)
	if err := lock.Release(ctx); err != nil {
		t.Fatal(err)
	}
	// Releasing the same handle twice is safe and sends no further command.
	if err := lock.Release(ctx); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)
}

func TestLockHeldAndExpired(t *testing.T) {
	t.Parallel()
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "held", true: "expired"}[expired], func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			acquired := fixedTime.UTC()
			if expired {
				acquired = acquired.Add(-2 * time.Minute)
			}
			info := LockInfo{Owner: "bob", PID: 42, Host: "workstation", Acquired: acquired, TTLSeconds: 60, Release: "old"}
			data, err := json.Marshal(info)
			if err != nil {
				t.Fatal(err)
			}
			f.Expect("mkdir").Return("", "exists", 1)
			f.Expect("sh", "-c").Return(string(data), "", 0)
			if expired {
				f.Expect("sh", "-c").Return("", "", 0)
				f.ExpectUpload("/opt/app/deploy.lock/owner.json").Return("", "", 0)
			}
			var warning bytes.Buffer
			lock, err := AcquireLock(context.Background(), f, "app", LockOptions{Owner: "alice", Release: "new", TTL: time.Minute, Now: func() time.Time { return fixedTime }, Warn: &warning})
			if expired {
				if err != nil || lock == nil || !strings.Contains(warning.String(), "expired") {
					t.Fatalf("takeover = %v, %v, %s", lock, err, warning.String())
				}
			} else {
				var locked *LockedError
				var user *ui.Error
				if !errors.As(err, &locked) || locked.Info.Owner != "bob" || !errors.As(err, &user) || user.Code != ui.ExitLocked || user.Try != "wait, or run spinup deploy unlock --force" {
					t.Fatalf("held error = %#v", err)
				}
			}
			f.AssertDone(t)
		})
	}
}

func TestLockReadAndForce(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	ctx := context.Background()
	f.Expect("sh", "-c").Return("", "", 3)
	info, err := ReadLock(ctx, f, "app")
	if info != nil || err != nil {
		t.Fatalf("absent = %v, %v", info, err)
	}
	f.Expect("rm", "-rf", "--", "/opt/app/deploy.lock", "/opt/app/deploy.lock.guard").Return("", "", 0)
	if err := ForceUnlock(ctx, f, "app"); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)
}

func TestReadLockInvalidMetadata(t *testing.T) {
	t.Parallel()
	for _, data := range []string{`not json`, `{}`, `{"acquired":"2026-10-15T07:00:00Z","ttl_seconds":0}`, `{"acquired":"2026-10-15T07:00:00Z","ttl_seconds":9223372036854775807}`} {
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("sh", "-c").Return(data, "", 0)
			if _, err := ReadLock(context.Background(), f, "app"); err == nil {
				t.Fatal("invalid lock metadata accepted")
			}
			f.AssertDone(t)
		})
	}
}

func TestLockFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("SSH failure", func(t *testing.T) {
		t.Parallel()
		f := &remote.Fake{}
		f.Expect("mkdir").Return("", "disconnected", 255)
		if _, err := AcquireLock(ctx, f, "app", LockOptions{}); err == nil {
			t.Fatal("SSH failure ignored")
		}
		f.AssertDone(t)
	})
	t.Run("owner upload failure", func(t *testing.T) {
		t.Parallel()
		f := &remote.Fake{}
		f.Expect("mkdir").Return("", "", 0)
		f.ExpectUpload("/opt/app/deploy.lock/owner.json").Fail(errors.New("disconnected"))
		if _, err := AcquireLock(ctx, f, "app", LockOptions{}); err == nil || !strings.Contains(err.Error(), "unlock --force") {
			t.Fatalf("upload failure = %v", err)
		}
		f.AssertDone(t)
	})
	t.Run("invalid input", func(t *testing.T) {
		t.Parallel()
		f := &remote.Fake{}
		for _, app := range []string{"", "../app", "app/name"} {
			if _, err := AcquireLock(ctx, f, app, LockOptions{}); err == nil {
				t.Fatalf("invalid app %q accepted", app)
			}
		}
		if _, err := AcquireLock(ctx, f, "app", LockOptions{TTL: -time.Second}); err == nil {
			t.Fatal("negative TTL accepted")
		}
		if len(f.Calls()) != 0 {
			t.Fatal("invalid input reached the VM")
		}
	})
}
