package testkit_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/testkit"
)

func migrations() fstest.MapFS {
	return fstest.MapFS{
		"00001_todos.sql": {Data: []byte(`-- +goose Up
CREATE TABLE todos (id bigint PRIMARY KEY, title text NOT NULL);
INSERT INTO todos VALUES (1, 'seeded');

-- +goose Down
DROP TABLE todos;
`)},
		"README.md": {Data: []byte("not a migration")},
	}
}

func currentDB(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var name string
	if err := pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func dbExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(),
		"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestDBIsEmptyAndDropped(t *testing.T) {
	t.Parallel()
	outer := testkit.DB(t)
	var inner string
	t.Run("inner", func(t *testing.T) {
		pool := testkit.DB(t)
		inner = currentDB(t, pool)
		if !strings.HasPrefix(inner, "t_testdbisemptyanddropped_inner_") || len(inner) > 63 {
			t.Errorf("database name %q", inner)
		}
		var tables int
		if err := pool.QueryRow(context.Background(),
			"SELECT count(*) FROM pg_tables WHERE schemaname = 'public'").Scan(&tables); err != nil {
			t.Fatal(err)
		}
		if tables != 0 {
			t.Errorf("public tables = %d, want 0", tables)
		}
		var lock string
		if err := pool.QueryRow(context.Background(), "SHOW lock_timeout").Scan(&lock); err != nil {
			t.Fatal(err)
		}
		if lock != "5s" {
			t.Errorf("lock_timeout = %q, want 5s", lock)
		}
		if url := testkit.URL(t, pool); !strings.Contains(url, inner) {
			t.Errorf("URL does not name the database %q", inner)
		}
	})
	if dbExists(t, outer, inner) {
		t.Errorf("database %s still exists after the test ended", inner)
	}
}

func TestLongTestNameFitsIdentifier(t *testing.T) {
	t.Parallel()
	t.Run(strings.Repeat("a very long subtest name ", 8), func(t *testing.T) {
		t.Parallel()
		name := currentDB(t, testkit.DB(t))
		if len(name) > 63 || !strings.HasPrefix(name, "t_") {
			t.Errorf("database name %q (%d bytes)", name, len(name))
		}
	})
}

// Parallel tests get migrated, independent clones of one template.
func TestDBWithMigrationsClones(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"first", "second", "third"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := testkit.DBWithMigrations(t, migrations())
			if _, err := pool.Exec(ctx, "INSERT INTO todos VALUES (2, $1)", name); err != nil {
				t.Fatal(err)
			}
			rows, err := pool.Query(ctx, "SELECT title FROM todos ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			var titles []string
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					t.Fatal(err)
				}
				titles = append(titles, s)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if got, want := strings.Join(titles, ","), "seeded,"+name; got != want {
				t.Errorf("titles = %q, want %q", got, want)
			}
		})
	}
}

func TestDoAndDecodeJSON(t *testing.T) {
	t.Parallel()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"method":"` + r.Method + `","path":"` + r.URL.Path + `","ct":"` + r.Header.Get("Content-Type") + `"}`))
	})
	type resp struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		CT     string `json:"ct"`
	}
	tests := []struct {
		method string
		body   string
		want   resp
	}{
		{http.MethodGet, "", resp{"GET", "/todos", ""}},
		{http.MethodPost, `{"title":"x"}`, resp{"POST", "/todos", "application/json"}},
	}
	for _, tt := range tests {
		var rec = testkit.Do(t, h, tt.method, "/todos", nil)
		if tt.body != "" {
			rec = testkit.Do(t, h, tt.method, "/todos", strings.NewReader(tt.body))
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		if got := testkit.DecodeJSON[resp](t, rec); got != tt.want {
			t.Errorf("got %+v, want %+v", got, tt.want)
		}
	}
}
