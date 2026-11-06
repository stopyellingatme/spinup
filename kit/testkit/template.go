package testkit

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"io/fs"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/db"
)

// templateSalt changes the template names when the build steps change.
const templateSalt = "spinup-testkit-template-v1"

// DBOption changes how [DBWithMigrations] builds a template database.
type DBOption func(*dbConfig)

type dbConfig struct {
	kitFS fs.FS
}

// WithKitMigrations applies the kit SQL files before the app migrations.
// Both file systems contribute to the template hash.
func WithKitMigrations(fsys fs.FS) DBOption {
	return func(c *dbConfig) { c.kitFS = fsys }
}

type templateEntry struct {
	once sync.Once
	err  error
}

var (
	templatesMu sync.Mutex
	templates   = map[string]*templateEntry{}
)

// DBWithMigrations returns a pool on a new database that holds the migrations
// in fsys (the *.sql files at its root). The first call for a migration set
// builds a template database. Each call then clones it with
// CREATE DATABASE ... TEMPLATE, which is fast. The database is dropped when
// the test ends.
//
// [WithKitMigrations] applies the kit space first.
// A template hash includes the file names and contents of both spaces.
// A finished template stays on the server, so later test runs and other test
// processes reuse it.
func DBWithMigrations(t testing.TB, fsys fs.FS, opts ...DBOption) *pgxpool.Pool {
	t.Helper()
	cfg := dbConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	s := getServer(t)
	hash, err := migrationsHash(fsys, cfg.kitFS)
	if err != nil {
		t.Fatalf("testkit: read migrations: %v", err)
	}
	name := "tpl_" + hash[:24]

	templatesMu.Lock()
	e, ok := templates[name]
	if !ok {
		e = &templateEntry{}
		templates[name] = e
	}
	templatesMu.Unlock()

	e.once.Do(func() { e.err = s.buildTemplate(name, hash, fsys, cfg.kitFS) })
	if e.err != nil {
		t.Fatalf("testkit: build template database %s: %v", name, e.err)
	}
	return s.newDatabase(t, name)
}

// migrationsHash hashes the file names and contents of both migration spaces.
// Files and spaces have explicit boundaries so neither can alias the other.
func migrationsHash(fsys, kitFS fs.FS) (string, error) {
	h := sha256.New()
	writeField(h, []byte(templateSalt))
	if err := hashMigrationFiles(h, fsys); err != nil {
		return "", err
	}
	if kitFS != nil {
		writeField(h, []byte("kit"))
		if err := hashMigrationFiles(h, kitFS); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashMigrationFiles(w io.Writer, fsys fs.FS) error {
	names, err := fs.Glob(fsys, "*.sql") // sorted
	if err != nil {
		return err
	}
	for _, n := range names {
		data, err := fs.ReadFile(fsys, n)
		if err != nil {
			return err
		}
		writeField(w, []byte(n))
		writeField(w, data)
	}
	return nil
}

// writeField writes a length-prefixed field, so field boundaries cannot shift.
func writeField(w io.Writer, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	_, _ = w.Write(n[:])
	_, _ = w.Write(b)
}

// buildTemplate creates the template database name when it does not exist.
// An advisory lock on the admin database keeps other test processes from
// building the same template at the same time. The build happens in a
// scratch database that is renamed at the end, so a template that exists is
// always complete.
func (s *server) buildTemplate(name, hash string, fsys, kitFS fs.FS) error {
	ctx, cancel := context.WithTimeout(context.Background(), adminTimeout)
	defer cancel()
	conn, err := s.admin.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	key, _ := hex.DecodeString(hash[:16])
	lockID := int64(binary.BigEndian.Uint64(key)) //nolint:gosec // any 64-bit value is a valid lock ID
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		return errorf("lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockID)
	}()

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}

	build := name + "_build"
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quote(build)+" WITH (FORCE)"); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+quote(build)); err != nil {
		return err
	}
	buildURL, err := withDatabase(s.url, build)
	if err != nil {
		return err
	}
	m, err := db.NewMigrator(buildURL, fsys, db.WithOutput(io.Discard), db.WithKitMigrations(kitFS))
	if err != nil {
		return err
	}
	upErr := m.KitUp(ctx)
	if upErr == nil {
		upErr = m.Up(ctx)
	}
	closeErr := m.Close()
	if upErr != nil {
		return upErr
	}
	if closeErr != nil {
		return closeErr
	}
	// Disconnect any idle sessions left on the scratch database, then publish it.
	if _, err := conn.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", build); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "ALTER DATABASE "+quote(build)+" RENAME TO "+quote(name)); err != nil {
		return err
	}
	_, err = conn.Exec(ctx, "ALTER DATABASE "+quote(name)+" WITH IS_TEMPLATE true ALLOW_CONNECTIONS false")
	return err
}
