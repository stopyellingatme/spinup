package db_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestOpenErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		url     string
		want    []string
		notWant string
	}{
		{
			name: "empty URL",
			url:  "",
			want: []string{"the database URL is empty", "\nTry: set DATABASE_URL (see .env.example)"},
		},
		{
			name:    "bad URL hides the password",
			url:     "postgres://app:s3cr3t-pw@db:notaport/app",
			want:    []string{"not valid", "\nTry: "},
			notWant: "s3cr3t-pw",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, err := db.Open(context.Background(), tt.url)
			if err == nil {
				pool.Close()
				t.Fatal("Open returned no error")
			}
			msg := err.Error()
			for _, w := range tt.want {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q does not contain %q", msg, w)
				}
			}
			if tt.notWant != "" && strings.Contains(msg, tt.notWant) {
				t.Errorf("error %q leaks %q", msg, tt.notWant)
			}
		})
	}
}

func TestOpenIsLazy(t *testing.T) {
	t.Parallel()
	// Nothing listens on port 1. Open must still succeed.
	pool, err := db.Open(context.Background(), "postgres://app:pw@127.0.0.1:1/app?sslmode=disable")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pool.Close()
}

func TestOpenRuntimeParams(t *testing.T) {
	t.Parallel()
	base := testkit.URL(t, testkit.DB(t))
	tests := []struct {
		name     string
		url      string
		opts     []db.Option
		want     map[string]string
		maxConns int32
	}{
		{
			name: "defaults",
			url:  base,
			want: map[string]string{"statement_timeout": "30s", "lock_timeout": "5s", "application_name": "spinup-app"},
		},
		{
			name: "options",
			url:  base,
			opts: []db.Option{
				db.WithStatementTimeout(2 * time.Second),
				db.WithLockTimeout(1500 * time.Millisecond),
				db.WithAppName("todo-api"),
				db.WithMaxConns(3),
			},
			want:     map[string]string{"statement_timeout": "2s", "lock_timeout": "1500ms", "application_name": "todo-api"},
			maxConns: 3,
		},
		{
			name: "URL value wins over the default",
			url:  withQuery(base, "lock_timeout=750"),
			want: map[string]string{"statement_timeout": "30s", "lock_timeout": "750ms"},
		},
		{
			name: "option wins over the URL value",
			url:  withQuery(base, "lock_timeout=750"),
			opts: []db.Option{db.WithLockTimeout(time.Second)},
			want: map[string]string{"lock_timeout": "1s"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool, err := db.Open(ctx, tt.url, tt.opts...)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer pool.Close()
			for param, want := range tt.want {
				if got := show(t, pool, param); got != want {
					t.Errorf("SHOW %s = %q, want %q", param, got, want)
				}
			}
			if tt.maxConns != 0 && pool.Config().MaxConns != tt.maxConns {
				t.Errorf("MaxConns = %d, want %d", pool.Config().MaxConns, tt.maxConns)
			}
		})
	}
}

func show(t *testing.T, pool *pgxpool.Pool, param string) string {
	t.Helper()
	var v string
	if err := pool.QueryRow(context.Background(), "SHOW "+param).Scan(&v); err != nil {
		t.Fatalf("SHOW %s: %v", param, err)
	}
	return v
}

func withQuery(url, kv string) string {
	if strings.Contains(url, "?") {
		return url + "&" + kv
	}
	return url + "?" + kv
}
