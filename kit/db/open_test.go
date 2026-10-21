package db_test

import (
	"context"
	"math"
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
			name:     "default pool size",
			url:      base,
			maxConns: db.DefaultMaxConns,
		},
		{
			name:     "URL pool size wins over the default",
			url:      withQuery(base, "pool_max_conns=7"),
			maxConns: 7,
		},
		{
			name:     "option pool size wins over the URL",
			url:      withQuery(base, "pool_max_conns=7"),
			opts:     []db.Option{db.WithMaxConns(9)},
			maxConns: 9,
		},
		{
			name:     "zero option keeps the URL pool size",
			url:      withQuery(base, "pool_max_conns=7"),
			opts:     []db.Option{db.WithMaxConns(0)},
			maxConns: 7,
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

func TestOpenRejectsBadMaxConns(t *testing.T) {
	t.Parallel()
	for _, n := range []int{-1, math.MaxInt32 + 1} {
		_, err := db.Open(context.Background(), "postgres://app:pw@127.0.0.1:1/app?sslmode=disable", db.WithMaxConns(n))
		if err == nil || !strings.Contains(err.Error(), "Try: set DATABASE_MAX_CONNS") {
			t.Errorf("WithMaxConns(%d): err = %v, want a DATABASE_MAX_CONNS hint", n, err)
		}
	}
}

func TestOpenKeywordMaxConns(t *testing.T) {
	t.Parallel()
	for conn, want := range map[string]int32{
		"host=127.0.0.1 port=1 user=app dbname=app sslmode=disable":                                db.DefaultMaxConns,
		"host=127.0.0.1 port=1 user=app dbname=app sslmode=disable pool_max_conns=3":               3,
		"pool_max_conns = 5 host=127.0.0.1 port=1 user=app dbname=app sslmode=disable":             5,
		"postgresql://app:pw@127.0.0.1:1/app?sslmode=disable&pool_max_conns=6":                     6,
		"host=127.0.0.1 port=1 user=app password='my pool_max_conns=3' dbname=app sslmode=disable": db.DefaultMaxConns,
	} {
		pool, err := db.Open(context.Background(), conn)
		if err != nil {
			t.Fatalf("Open(%q): %v", conn, err)
		}
		if got := pool.Config().MaxConns; got != want {
			t.Errorf("Open(%q): MaxConns = %d, want %d", conn, got, want)
		}
		pool.Close()
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
