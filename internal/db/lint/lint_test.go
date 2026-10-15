package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{"drop column", "ALTER TABLE todos DROP COLUMN old_title;", "drop-column"},
		{"drop column without column", "ALTER TABLE todos DROP old_title;", "drop-column"},
		{"drop table", "DROP TABLE todos;", "drop-table"},
		{"alter type", "ALTER TABLE todos ALTER COLUMN title TYPE bigint;", "alter-type"},
		{"alter type without column", "ALTER TABLE todos ALTER title TYPE bigint;", "alter-type"},
		{"set data type", "ALTER TABLE todos ALTER COLUMN title SET DATA TYPE bigint;", "alter-type"},
		{"set not null", "ALTER TABLE todos ALTER COLUMN title SET NOT NULL;", "set-not-null"},
		{"set not null without column", "ALTER TABLE todos ALTER title SET NOT NULL;", "set-not-null"},
		{"add not null", "ALTER TABLE todos ADD COLUMN title text NOT NULL;", "set-not-null"},
		{"add not null without column", "ALTER TABLE todos ADD title text NOT NULL;", "set-not-null"},
		{"add clauses have separate defaults", "ALTER TABLE todos ADD COLUMN a int DEFAULT 1, ADD b int NOT NULL;", "set-not-null"},
		{"do block", "DO $$ BEGIN DROP TABLE todos; END $$;", "unchecked-dynamic-sql"},
		{"rename", "ALTER TABLE todos RENAME COLUMN title TO name;", "rename"},
		{"concurrent transaction", "CREATE INDEX CONCURRENTLY todos_title_idx ON todos (title);", "concurrent-in-tx"},
		{"comments strings and dollars", "-- DROP TABLE ignored\nSELECT 'DROP TABLE ignored';\nSELECT $$ALTER TABLE todos DROP COLUMN ignored$$;", ""},
		{"down ignored", "CREATE TABLE todos (id int);\n-- +goose Down\nDROP TABLE todos;", ""},
		{"statement block", "-- +goose StatementBegin\nALTER TABLE todos DROP COLUMN old_title;\n-- +goose StatementEnd", "drop-column"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Lint("00001_test.sql", goose(tc.sql))
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("findings = %#v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Rule != tc.want || got[0].Line < 2 || got[0].Try == "" {
				t.Fatalf("findings = %#v", got)
			}
		})
	}
}

func TestLintSafeCases(t *testing.T) {
	t.Parallel()
	for _, sql := range []string{
		"ALTER TABLE todos ADD COLUMN old_title text;",
		"CREATE TABLE archived_todos (id int);",
		"ALTER TABLE todos ALTER COLUMN title SET DEFAULT '';",
		"ALTER TABLE todos ADD COLUMN title text NOT NULL DEFAULT '';",
		"ALTER TABLE todos ADD CONSTRAINT todos_title_key UNIQUE (title);",
		"ALTER TABLE todos DROP CONSTRAINT todos_title_key;",
		"ALTER TABLE todos DROP DEFAULT;",
		"ALTER TABLE todos DROP NOT NULL;",
		"ALTER TABLE todos ADD COLUMN code int GENERATED ALWAYS AS (id) STORED NOT NULL;",
		"ALTER TABLE todos ADD COLUMN renamed_title text;",
		"CREATE TABLE todos (id int);\nCREATE INDEX todos_id_idx ON todos (id);",
		"-- +goose NO TRANSACTION\nCREATE INDEX CONCURRENTLY todos_id_idx ON todos (id);",
	} {
		got, err := Lint("00001_test.sql", goose(sql))
		if err != nil || len(got) != 0 {
			t.Fatalf("Lint() = %#v, %v", got, err)
		}
	}
}

func TestLintOverride(t *testing.T) {
	t.Parallel()
	allowed, err := Lint("00001_test.sql", goose("-- spinup:allow-destructive archived data\nDROP TABLE todos;"))
	if err != nil || len(allowed) != 0 {
		t.Fatalf("allowed = %#v, %v", allowed, err)
	}
	bad, err := Lint("00001_test.sql", goose("-- spinup:allow-destructive\nDROP TABLE todos;"))
	if err != nil || len(bad) != 2 || bad[0].Rule != "bad-override" || bad[1].Rule != "drop-table" {
		t.Fatalf("bad = %#v, %v", bad, err)
	}
}

func TestLintDynamicOverride(t *testing.T) {
	t.Parallel()
	got, err := Lint("00001_test.sql", goose("-- spinup:allow-destructive audited migration\nDO $$ BEGIN DROP TABLE todos; END $$;"))
	if err != nil || len(got) != 0 {
		t.Fatalf("Lint() = %#v, %v", got, err)
	}
}

func TestLintDirIndexOnEarlierTable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeMigration(t, dir, "00001_create_todos.sql", "CREATE TABLE todos (id int);")
	writeMigration(t, dir, "00002_index_todos.sql", "CREATE INDEX todos_id_idx ON todos (id);")
	got, err := LintDir(dir)
	if err != nil || len(got) != 1 || got[0].Rule != "index-not-concurrent" || got[0].File != "00002_index_todos.sql" {
		t.Fatalf("LintDir() = %#v, %v", got, err)
	}
}

func TestLintDirRecognizesQualifiedAndModifiedTables(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		create string
		index  string
	}{
		{"schema", "CREATE TABLE public.todos (id int);", "CREATE INDEX todos_id_idx ON todos (id);"},
		{"unlogged", "CREATE UNLOGGED TABLE todos (id int);", "CREATE INDEX todos_id_idx ON todos (id);"},
		{"temporary", "CREATE TEMP TABLE todos (id int);", "CREATE INDEX todos_id_idx ON todos (id);"},
		{"only", "CREATE TABLE todos (id int);", "CREATE UNIQUE INDEX todos_id_idx ON ONLY todos (id);"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeMigration(t, dir, "00001_create_todos.sql", tc.create)
			writeMigration(t, dir, "00002_index_todos.sql", tc.index)
			got, err := LintDir(dir)
			if err != nil || len(got) != 1 || got[0].Rule != "index-not-concurrent" {
				t.Fatalf("LintDir() = %#v, %v", got, err)
			}
		})
	}
}

func TestLintFlagsIndexWithoutHistory(t *testing.T) {
	t.Parallel()
	got, err := Lint("00002_index_todos.sql", goose("CREATE INDEX todos_id_idx ON todos (id);"))
	if err != nil || len(got) != 1 || got[0].Rule != "index-not-concurrent" {
		t.Fatalf("Lint() = %#v, %v", got, err)
	}
}

func FuzzLint(f *testing.F) {
	for _, seed := range []string{"-- +goose Up\nDROP TABLE todos;", "-- +goose Up\nSELECT $$;$$;"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		_, _ = Lint("00001_fuzz.sql", []byte(sql))
	})
}

func goose(sql string) []byte { return []byte("-- +goose Up\n" + sql + "\n-- +goose Down\n") }

func writeMigration(t *testing.T, dir, name, sql string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), goose(sql), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLintErrors(t *testing.T) {
	t.Parallel()
	_, err := LintDir(filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "read migrations directory") {
		t.Fatalf("error = %v", err)
	}
}
