package db_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func threeMigrations() fstest.MapFS {
	fsys := migrationsFS()
	fsys["00003_tags.sql"] = &fstest.MapFile{Data: []byte(`-- +goose Up
CREATE TABLE tags (id bigint PRIMARY KEY);

-- +goose Down
DROP TABLE tags;
`)}
	return fsys
}

func TestMigrateCommandRedoToVersion(t *testing.T) {
	t.Parallel()
	url := testkit.URL(t, testkit.DB(t))
	fsys := threeMigrations()
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"version"}, "0\n"},
		{[]string{"redo"}, "migrate: nothing to redo\n"},
		{[]string{"to", "2"}, "applied  00001_users.sql\napplied  00002_notes.sql\n"},
		{[]string{"version"}, "2\n"},
		{[]string{"to", "2"}, "migrate: already at version 2\n"},
		{[]string{"redo"}, "rolled back  00002_notes.sql\napplied  00002_notes.sql\n"},
		{[]string{"status"}, "applied  00001_users.sql\napplied  00002_notes.sql\npending  00003_tags.sql\n"},
		{[]string{"to", "3"}, "applied  00003_tags.sql\n"},
		{[]string{"to", "1"}, "rolled back  00003_tags.sql\nrolled back  00002_notes.sql\n"},
		{[]string{"version"}, "1\n"},
		{[]string{"to", "0"}, "rolled back  00001_users.sql\n"},
		{[]string{"version"}, "0\n"},
	}
	for i, s := range steps {
		if got := runCmd(t, url, fsys, s.args...); got != s.want {
			t.Fatalf("step %d: migrate %v output:\n%s\nwant:\n%s", i, s.args, got, s.want)
		}
	}
}

func TestMigrateCommandToUnknownVersion(t *testing.T) {
	t.Parallel()
	url := testkit.URL(t, testkit.DB(t))
	err := db.MigrateCommand(migrationsFS(), db.WithDatabaseURL(url), db.WithOutput(&bytes.Buffer{}))(context.Background(), []string{"to", "9"})
	if err == nil || !strings.Contains(err.Error(), "no migration file has version 9") || !strings.Contains(err.Error(), "\nTry: ") {
		t.Fatalf("error = %v", err)
	}
}

func TestMigrateCommandNoMigrationsRedoToVersion(t *testing.T) {
	t.Parallel()
	url := testkit.URL(t, testkit.DB(t))
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"redo"}, "migrate: nothing to redo\n"},
		{[]string{"version"}, "0\n"},
		{[]string{"to", "0"}, "migrate: already at version 0\n"},
	} {
		if got := runCmd(t, url, fstest.MapFS{}, tt.args...); got != tt.want {
			t.Errorf("migrate %v output %q, want %q", tt.args, got, tt.want)
		}
	}
}

// hookLog records hook calls in order.
type hookLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *hookLog) hook(name string, fail error) db.Hook {
	return db.Hook{
		AfterUp: func(_ context.Context, url string) error {
			l.add(name + ".AfterUp")
			if url == "" {
				return errors.New("empty url")
			}
			return fail
		},
		BeforeDown: func(_ context.Context, url string) error {
			l.add(name + ".BeforeDown")
			if url == "" {
				return errors.New("empty url")
			}
			return fail
		},
	}
}

func (l *hookLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, s)
}

func (l *hookLog) take() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := strings.Join(l.calls, ",")
	l.calls = nil
	return s
}

func TestMigrateCommandHooks(t *testing.T) {
	t.Parallel()
	url := testkit.URL(t, testkit.DB(t))
	fsys := threeMigrations()
	log := &hookLog{}
	opts := []db.MigrateOption{
		db.WithDatabaseURL(url),
		db.WithOutput(&bytes.Buffer{}),
		db.WithHook(log.hook("a", nil)),
		db.WithHook(db.Hook{}), // nil functions are skipped
		db.WithHook(log.hook("b", nil)),
	}
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"status"}, ""},
		{[]string{"version"}, ""},
		{[]string{"up"}, "a.AfterUp,b.AfterUp"},
		{[]string{"up"}, "a.AfterUp,b.AfterUp"},
		{[]string{"redo"}, "a.BeforeDown,b.BeforeDown,a.AfterUp,b.AfterUp"},
		{[]string{"down"}, "a.BeforeDown,b.BeforeDown"},
		{[]string{"to", "1"}, "a.BeforeDown,b.BeforeDown"},
		{[]string{"to", "1"}, ""},
		{[]string{"to", "3"}, "a.AfterUp,b.AfterUp"},
	}
	for i, s := range steps {
		if err := db.MigrateCommand(fsys, opts...)(context.Background(), s.args); err != nil {
			t.Fatalf("step %d: migrate %v: %v", i, s.args, err)
		}
		if got := log.take(); got != s.want {
			t.Fatalf("step %d: migrate %v hooks %q, want %q", i, s.args, got, s.want)
		}
	}
}

func TestMigrateCommandHookErrors(t *testing.T) {
	t.Parallel()
	url := testkit.URL(t, testkit.DB(t))
	boom := errors.New("boom")
	for _, tt := range []struct {
		args    []string
		calls   string
		want    string
		version string
	}{
		// AfterUp fails after the app migrations are applied.
		{[]string{"up"}, "a.AfterUp,b.AfterUp", "AfterUp of migration hook 2 of 2 failed: boom", "3\n"},
		// BeforeDown fails before the app migrations move.
		{[]string{"down"}, "a.BeforeDown,b.BeforeDown", "BeforeDown of migration hook 2 of 2 failed: boom", "3\n"},
		{[]string{"to", "1"}, "a.BeforeDown,b.BeforeDown", "BeforeDown of migration hook 2 of 2 failed: boom", "3\n"},
	} {
		log := &hookLog{}
		cmd := db.MigrateCommand(threeMigrations(), db.WithDatabaseURL(url), db.WithOutput(&bytes.Buffer{}),
			db.WithHook(log.hook("a", nil)), db.WithHook(log.hook("b", boom)))
		err := cmd(context.Background(), tt.args)
		if err == nil || !errors.Is(err, boom) || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "\nTry: ") {
			t.Fatalf("migrate %v: error = %v, want %q", tt.args, err, tt.want)
		}
		if got := log.take(); got != tt.calls {
			t.Errorf("migrate %v hooks %q, want %q", tt.args, got, tt.calls)
		}
		if got := runCmd(t, url, threeMigrations(), "version"); got != tt.version {
			t.Errorf("after migrate %v: version %q, want %q", tt.args, got, tt.version)
		}
	}
}
