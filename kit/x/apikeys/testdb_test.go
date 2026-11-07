package apikeys

import (
	"context"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/testkit"
)

func keyDB(t *testing.T, withAudit bool) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is empty")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Skipf("PostgreSQL is unavailable: %v", err)
	}
	_ = conn.Close(ctx)
	files := map[string][]string{
		"auth":    {"00002_create_auth.sql", "00005_users_role.sql", "00007_users_auth_version.sql"},
		"apikeys": {"00009_api_keys.sql"},
	}
	if withAudit {
		files["audit"] = []string{"00006_audit_events.sql"}
	}
	fsys := fstest.MapFS{}
	for pack, names := range files {
		for _, name := range names {
			data, err := os.ReadFile("../../../internal/templates/packs/" + pack + "/files/api/db/kit/" + name)
			if err != nil {
				t.Fatal(err)
			}
			fsys[name] = &fstest.MapFile{Data: data, Mode: 0o644}
		}
	}
	return testkit.DBWithMigrations(t, fstest.MapFS{}, testkit.WithKitMigrations(fsys))
}

func dbStore(t *testing.T, pool *pgxpool.Pool, cfg Config, opts ...StoreOption) *Store {
	t.Helper()
	s, err := NewStore(pool, cfg, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func keyUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,password_hash,role) VALUES($1,'unused','manager') RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
