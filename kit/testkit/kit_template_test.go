package testkit_test

import (
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestDBWithKitMigrations(t *testing.T) {
	t.Parallel()
	kit := fstest.MapFS{"00002_users.sql": {Data: []byte("-- +goose Up\nCREATE TABLE users (id bigint PRIMARY KEY);\n-- +goose Down\nDROP TABLE users;\n")}}
	app := fstest.MapFS{"00002_notes.sql": {Data: []byte("-- +goose Up\nCREATE TABLE notes (user_id bigint REFERENCES users(id));\n-- +goose Down\nDROP TABLE notes;\n")}}
	for _, name := range []string{"first", "reuse", "changed kit"} {
		t.Run(name, func(t *testing.T) {
			if name == "changed kit" {
				kit["00003_seed.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\nINSERT INTO users VALUES (1);\n-- +goose Down\nDELETE FROM users WHERE id = 1;\n")}
			}
			pool := testkit.DBWithMigrations(t, app, testkit.WithKitMigrations(kit))
			var appVersion, kitVersion, count int
			if err := pool.QueryRow(t.Context(), "SELECT max(version_id) FROM goose_db_version").Scan(&appVersion); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(t.Context(), "SELECT max(version_id) FROM spinup_kit_version").Scan(&kitVersion); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&count); err != nil {
				t.Fatal(err)
			}
			wantKit, wantCount := 2, 0
			if name == "changed kit" {
				wantKit, wantCount = 3, 1
			}
			if appVersion != 2 || kitVersion != wantKit || count != wantCount {
				t.Fatalf("app = %d, kit = %d, users = %d", appVersion, kitVersion, count)
			}
		})
	}
}
