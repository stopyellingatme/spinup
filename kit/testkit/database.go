package testkit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/db"
)

const (
	maxIdentLen   = 63 // PostgreSQL NAMEDATALEN - 1
	maxNamePart   = 32 // bytes of the test name in a database name
	adminTimeout  = 2 * time.Minute
	cloneAttempts = 50
	cloneBackoff  = 100 * time.Millisecond
)

var dbCounter atomic.Int64

// DB returns a pool on a new, empty database for this test. The database is
// dropped when the test ends.
func DB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	s := getServer(t)
	return s.newDatabase(t, "")
}

// URL returns the connection URL of the test database behind pool.
func URL(t testing.TB, pool *pgxpool.Pool) string {
	t.Helper()
	u := pool.Config().ConnString()
	if u == "" {
		t.Fatal("testkit: URL: the pool has no connection string (use a pool from testkit.DB)")
	}
	return u
}

// newDatabase creates a database for t, from template when it is not empty,
// and returns a pool on it. A cleanup closes the pool and drops the database.
func (s *server) newDatabase(t testing.TB, template string) *pgxpool.Pool {
	t.Helper()
	name := dbName(t.Name(), os.Getpid(), dbCounter.Add(1))
	ctx, cancel := context.WithTimeout(context.Background(), adminTimeout)
	defer cancel()
	sql := "CREATE DATABASE " + quote(name)
	if template != "" {
		sql += " TEMPLATE " + quote(template)
	}
	if err := execRetry(ctx, s.admin, sql); err != nil {
		t.Fatalf("testkit: create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), adminTimeout)
		defer cancel()
		if _, err := s.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+quote(name)+" WITH (FORCE)"); err != nil {
			t.Logf("testkit: drop database %s: %v", name, err)
		}
	})
	dbURL, err := withDatabase(s.url, name)
	if err != nil {
		t.Fatalf("testkit: %v", err)
	}
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Fatalf("testkit: %v", err)
	}
	t.Cleanup(pool.Close) // runs before the drop: cleanups run last-in, first-out
	return pool
}

// execRetry runs sql. It retries while the template database is busy, for
// example when another process is still building or cloning it.
func execRetry(ctx context.Context, pool *pgxpool.Pool, sql string) error {
	var err error
	for range cloneAttempts {
		if _, err = pool.Exec(ctx, sql); err == nil {
			return nil
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55006" { // object_in_use
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(cloneBackoff):
		}
	}
	return err
}

// dbName returns "t_" plus the sanitized test name, the process ID, and a
// counter. The process ID keeps names unique when several test processes
// share one server. The result is at most 63 bytes.
func dbName(testName string, pid int, n int64) string {
	suffix := "_" + strconv.Itoa(pid) + "_" + strconv.FormatInt(n, 10)
	part := sanitize(testName)
	limit := min(maxNamePart, maxIdentLen-len("t_")-len(suffix))
	if len(part) > limit {
		part = part[:limit]
	}
	return "t_" + part + suffix
}

// sanitize lowercases s and maps every byte outside [a-z0-9] to "_",
// with no repeated "_" and none at either end.
func sanitize(s string) string {
	var b strings.Builder
	under := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			under = false
			continue
		}
		if !under && b.Len() > 0 {
			b.WriteByte('_')
			under = true
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

// withDatabase returns connString with its database set to name.
func withDatabase(connString, name string) (string, error) {
	if strings.HasPrefix(connString, "postgres://") || strings.HasPrefix(connString, "postgresql://") {
		u, err := url.Parse(connString)
		if err != nil {
			return "", errors.New("DATABASE_URL is not a valid URL (it is not shown because it can hold a password)")
		}
		u.Path = "/" + name
		u.RawPath = ""
		return u.String(), nil
	}
	// Keyword/value form: a later keyword wins.
	return connString + " dbname=" + name, nil
}

func quote(ident string) string {
	return pgx.Identifier{ident}.Sanitize()
}

// errorf formats an error with a "testkit:" prefix.
func errorf(format string, args ...any) error {
	return fmt.Errorf("testkit: "+format, args...)
}
