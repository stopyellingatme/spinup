package selfupdate

import (
	"context"
	"net/http"

	"sync/atomic"
	"testing"
	"time"
)

func TestNotice(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                             string
		tty, json, offline, ci, disabled bool
		want                             int32
	}{
		{name: "daily", tty: true, want: 2}, {name: "not a terminal"}, {name: "json", tty: true, json: true},
		{name: "offline", tty: true, offline: true}, {name: "ci", tty: true, ci: true}, {name: "disabled", tty: true, disabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var count atomic.Int32
			srv := newTestHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				count.Add(1)
				_, _ = w.Write([]byte(`[{"tag_name":"v1.2.0"}]`))
			}))
			defer srv.Close()
			u := Updater{APIBase: srv.URL, Client: srv.Client()}
			now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
			o := NoticeOptions{Current: "1.0.0", TTY: tt.tty, JSON: tt.json, Offline: tt.offline, CI: tt.ci, Disabled: tt.disabled, CacheDir: t.TempDir(), Now: now}
			first := u.Notice(context.Background(), o)
			if tt.want > 0 && first == "" {
				t.Fatal("missing notice")
			}
			o.Now = now.Add(23 * time.Hour)
			if got := u.Notice(context.Background(), o); got != "" {
				t.Fatal("notice was not throttled")
			}
			o.Now = now.Add(24 * time.Hour)
			u.Notice(context.Background(), o)
			if count.Load() != tt.want {
				t.Fatalf("requests=%d, want %d", count.Load(), tt.want)
			}
		})
	}
}

func TestNoticeFailure(t *testing.T) {
	t.Parallel()
	var count atomic.Int32
	srv := newTestHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		http.Error(w, "failed", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	u := Updater{APIBase: srv.URL, Client: srv.Client()}
	o := NoticeOptions{Current: "1.0.0", TTY: true, CacheDir: t.TempDir()}
	for range 2 {
		if got := u.Notice(context.Background(), o); got != "" {
			t.Fatalf("failure notice=%q", got)
		}
	}
	if count.Load() != 1 {
		t.Fatalf("requests=%d", count.Load())
	}
}

func TestNoticeDeadline(t *testing.T) {
	t.Parallel()
	srv := newTestHTTP(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	start := time.Now()
	if got := (Updater{APIBase: srv.URL, Client: srv.Client()}).Notice(context.Background(), NoticeOptions{Current: "1.0.0", TTY: true, CacheDir: t.TempDir()}); got != "" {
		t.Fatal(got)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("notice exceeded deadline: %s", elapsed)
	}
}
