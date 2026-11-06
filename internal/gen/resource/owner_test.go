package resource

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/db/lint"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// apply writes the files of plan to root.
func apply(t *testing.T, root string, plan vfs.Plan) {
	t.Helper()
	files := map[string]string{}
	for _, f := range plan.Files {
		files[f.Path] = string(f.Data)
	}
	write(t, root, files)
}

// ownedApp returns an app with the auth module and an owned projects
// resource, written to disk.
func ownedApp(t *testing.T) string {
	t.Helper()
	root := renderApp(t, []string{"base", "api", "web", "auth"}, []string{"auth"})
	out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"project", "name:string"}, Flags: flagSet(t, "--api-only")})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, root, out.Plan)
	return root
}

var ownedRefArgs = []string{"note", "title:string", "project:ref:project", "parent:ref:note?"}

// TestGoldenGenResourceOwnedRef covers an owned resource that refers to
// another owned resource and to itself, in an app that has server.go.
func TestGoldenGenResourceOwnedRef(t *testing.T) {
	t.Parallel()
	if !testutil.GoldenSelected(t, "gen-resource-owned-ref") {
		return
	}
	root := ownedApp(t)
	render := func() (fs.FS, error) {
		out, err := run(t.Context(), gen.Input{Root: root, Args: ownedRefArgs, Flags: flagSet(t, "--api-only")})
		if err != nil {
			return nil, err
		}
		got := fstest.MapFS{}
		for _, f := range out.Plan.Files {
			got[f.Path] = &fstest.MapFile{Data: f.Data, Mode: 0o644}
		}
		return got, nil
	}
	got, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "gen-resource-owned-ref", got)
	testutil.Deterministic(t, render)
}

func TestOwnerRefs(t *testing.T) {
	t.Parallel()
	const oldProjects = "-- +goose Up\nCREATE TABLE projects (\n    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,\n    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,\n    name text NOT NULL\n);\nCREATE INDEX projects_user_id_id_idx ON projects (user_id, id);\n\n-- +goose Down\nDROP TABLE projects;\n"
	const members = "-- +goose Up\nCREATE TABLE members (\n    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY\n);\n-- +goose Down\nDROP TABLE members;\n"
	tests := []struct {
		name   string
		setup  func(t *testing.T) string
		flags  []string
		args   []string
		want   []string
		reject []string
	}{
		{
			name:  "owned parent with a unique key",
			setup: ownedApp,
			args:  ownedRefArgs,
			want: []string{
				"    project_id bigint NOT NULL,\n",
				"    parent_id bigint,\n",
				"    CONSTRAINT notes_project_id_fkey FOREIGN KEY (project_id, user_id) REFERENCES projects (id, user_id)\n);\n",
				"CREATE UNIQUE INDEX notes_user_id_id_idx ON notes (user_id, id);\n",
				"ALTER TABLE notes ADD CONSTRAINT notes_parent_id_fkey FOREIGN KEY (parent_id, user_id) REFERENCES notes (id, user_id);\n",
			},
			reject: []string{"REFERENCES projects (id)\n", "CREATE UNIQUE INDEX IF NOT EXISTS"},
		},
		{
			name: "owned parent from an older generator",
			setup: func(t *testing.T) string {
				root := renderApp(t, []string{"base", "api", "web", "auth"}, []string{"auth"})
				write(t, root, map[string]string{"api/db/migrations/00008_create_projects.sql": oldProjects})
				return root
			},
			args: []string{"note", "title:string", "project:ref:project", "backup:ref:project?"},
			want: []string{
				"-- +goose Up\n-- spinup:allow-destructive the owner check of notes.project_id needs a unique (user_id, id) key on projects\nCREATE UNIQUE INDEX IF NOT EXISTS projects_user_id_id_key ON projects (user_id, id);\nCREATE TABLE notes (\n",
				"CONSTRAINT notes_backup_id_fkey FOREIGN KEY (backup_id, user_id) REFERENCES projects (id, user_id)",
			},
		},
		{
			name: "parent without an owner",
			setup: func(t *testing.T) string {
				root := renderApp(t, []string{"base", "api", "web", "auth"}, []string{"auth"})
				write(t, root, map[string]string{"api/db/migrations/00008_create_members.sql": members})
				return root
			},
			args:   []string{"note", "title:string", "member:ref:member"},
			want:   []string{"    member_id bigint NOT NULL REFERENCES members (id),\n"},
			reject: []string{"FOREIGN KEY", "spinup:allow-destructive"},
		},
		{
			name:   "shared resource",
			setup:  ownedApp,
			flags:  []string{"--shared"},
			args:   []string{"note", "title:string", "project:ref:project"},
			want:   []string{"    project_id bigint NOT NULL REFERENCES projects (id),\n"},
			reject: []string{"FOREIGN KEY", "user_id"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := tc.setup(t)
			out, err := run(t.Context(), gen.Input{Root: root, Args: tc.args, Flags: flagSet(t, append([]string{"--api-only"}, tc.flags...)...)})
			if err != nil {
				t.Fatal(err)
			}
			var migration vfs.File
			for _, f := range out.Plan.Files {
				if strings.HasSuffix(f.Path, "_create_notes.sql") {
					migration = f
				}
			}
			for _, want := range tc.want {
				if !strings.Contains(string(migration.Data), want) {
					t.Errorf("migration lacks %q:\n%s", want, migration.Data)
				}
			}
			for _, reject := range tc.reject {
				if strings.Contains(string(migration.Data), reject) {
					t.Errorf("migration has %q:\n%s", reject, migration.Data)
				}
			}
			// The generated migrations pass spinup db lint.
			apply(t, root, out.Plan)
			findings, err := lint.LintDir(filepath.Join(root, "api", "db", "migrations"))
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) > 0 {
				t.Fatalf("db lint findings: %+v", findings)
			}
		})
	}
}

func TestOwnerRefHandlers(t *testing.T) {
	t.Parallel()
	root := ownedApp(t)
	out, err := run(t.Context(), gen.Input{Root: root, Args: ownedRefArgs, Flags: flagSet(t, "--api-only")})
	if err != nil {
		t.Fatal(err)
	}
	handlers, _ := out.Plan.Get("api/internal/handlers/notes.go")
	for _, want := range []string{
		"return nil, noteRefError(err)",
		`case "notes_project_id_fkey":`,
		`return invalid([]httpx.FieldError{{Field: "project_id", Message: "must refer to an existing record"}})`,
		`case "notes_parent_id_fkey":`,
	} {
		if strings.Count(string(handlers.Data), want) == 0 {
			t.Errorf("handlers lack %q:\n%s", want, handlers.Data)
		}
	}
	if got := strings.Count(string(handlers.Data), "noteRefError(err)"); got != 2 {
		t.Errorf("noteRefError calls = %d, want 2 (create and update)", got)
	}
	test, _ := out.Plan.Get("api/internal/handlers/notes_test.go")
	for _, want := range []string{
		"INSERT INTO projects (user_id, name) VALUES ($1, 'sample') RETURNING id`, userA).Scan(&projectID)",
		"INSERT INTO projects (user_id, name) VALUES ($1, 'sample') RETURNING id`, userB).Scan(&otherProjectID)",
		`"create with project_id of user A", http.MethodPost, "/api/notes", fmt.Sprintf(` + "`" + `{"title": "title 1", "project_id": %d, "parent_id": null}` + "`" + `, projectID), "project_id"}`,
		`"create with parent_id of user A", http.MethodPost, "/api/notes", fmt.Sprintf(` + "`" + `{"title": "title 1", "project_id": %d, "parent_id": %d}` + "`" + `, otherProjectID, created.Id), "parent_id"}`,
		`"update with project_id of user A", http.MethodPut, otherPath,`,
		"int64(-1)",
		"DELETE FROM projects WHERE id = $1`, projectID)",
	} {
		if !strings.Contains(string(test.Data), want) {
			t.Errorf("handler test lacks %q:\n%s", want, test.Data)
		}
	}
	// server.go exists, so this resource does not test it.
	if strings.Contains(string(test.Data), "ResponseErrors") {
		t.Error("a later resource tests server.go")
	}
	if !strings.Contains(string(test.Data), `"offset too large"`) {
		t.Error("handler test lacks the offset bound")
	}
}

func TestOffsetTestFollowsServer(t *testing.T) {
	t.Parallel()
	root := freshApp(t)
	out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"todo", "title:string"}, Flags: flagSet(t)})
	if err != nil {
		t.Fatal(err)
	}
	test, _ := out.Plan.Get("api/internal/handlers/todos_test.go")
	for _, want := range []string{`"offset too large"`, "func TestTodosResponseErrors(t *testing.T) {"} {
		if !strings.Contains(string(test.Data), want) {
			t.Errorf("first resource test lacks %q", want)
		}
	}
	server, _ := out.Plan.Get(serverPath)
	for _, want := range []string{"maxOffset    = 10000", "o > maxOffset", "logx.FromContext(ctx)", "context.Canceled", "http.StatusServiceUnavailable"} {
		if !strings.Contains(string(server.Data), want) {
			t.Errorf("server.go lacks %q", want)
		}
	}
	if strings.Contains(string(server.Data), "slog.ErrorContext") {
		t.Error("server.go logs through the default slog logger")
	}

	// An app that owns an older server.go keeps its tests passing.
	write(t, root, map[string]string{serverPath: "package handlers\n"})
	out, err = run(t.Context(), gen.Input{Root: root, Args: []string{"note", "title:string"}, Flags: flagSet(t)})
	if err != nil {
		t.Fatal(err)
	}
	test, _ = out.Plan.Get("api/internal/handlers/notes_test.go")
	for _, reject := range []string{`"offset too large"`, "ResponseErrors"} {
		if strings.Contains(string(test.Data), reject) {
			t.Errorf("test for an older server.go has %q", reject)
		}
	}
}

func TestListQueryTotal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		owned bool
		want  string
	}{
		{"plain", false, "SELECT sqlc.embed(todos), (SELECT count(*) FROM todos AS counted)::bigint AS total\nFROM todos\nORDER BY id\n"},
		{"owned", true, "SELECT sqlc.embed(todos), (SELECT count(*) FROM todos AS counted WHERE counted.user_id = sqlc.arg(user_id))::bigint AS total\nFROM todos\nWHERE user_id = sqlc.arg(user_id)\nORDER BY id\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec, err := ParseSpec(t.TempDir(), []string{"todo", "title:string"}, "resource")
			if err != nil {
				t.Fatal(err)
			}
			spec.Owned = tc.owned
			got := string(queriesSQL(spec))
			if !strings.Contains(got, tc.want) || !strings.Contains(got, "-- name: CountTodos :one\n") {
				t.Fatalf("queries:\n%s\nwant to contain:\n%s", got, tc.want)
			}
			rc := newResourceCode(spec, "example.com/todo/api", "")
			handlers, err := rc.handlersGo()
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"total = rows[0].Total", "} else if offset > 0 {", "todoFromStore(row.Todo)"} {
				if !strings.Contains(string(handlers), want) {
					t.Errorf("handlers lack %q:\n%s", want, handlers)
				}
			}
		})
	}
}

func TestParentInsertsOwned(t *testing.T) {
	t.Parallel()
	root := ownedApp(t)
	out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"task", "title:string", "project:ref:project"}, Flags: flagSet(t, "--api-only")})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, root, out.Plan)
	// A grandchild finds the parent of the composite owner key.
	spec, err := ParseSpec(root, []string{"step", "task:ref:task"}, "resource")
	if err != nil {
		t.Fatal(err)
	}
	got, err := parentInserts(root, spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []parentInsert{
		{Table: "projects", SQL: "INSERT INTO projects (user_id, name) VALUES ($1, 'sample')", Owned: true},
		{Table: "tasks", SQL: "INSERT INTO tasks (user_id, title, project_id) VALUES ($1, 'sample', (SELECT max(id) FROM projects))", Owned: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parentInserts:\n got %+v\nwant %+v", got, want)
	}
}

func TestHasOwnerKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, migration string
		want                  bool
	}{
		{"inline unique", "id bigint, user_id bigint, UNIQUE (id, user_id)", "", true},
		{"unique index", "id bigint", "CREATE UNIQUE INDEX IF NOT EXISTS p_key ON public.projects (user_id, id);", true},
		{"plain index", "id bigint", "CREATE INDEX projects_user_id_id_idx ON projects (user_id, id);", false},
		{"other table", "id bigint", "CREATE UNIQUE INDEX x ON tasks (user_id, id);", false},
		{"in Down only", "id bigint", "-- +goose Down\nCREATE UNIQUE INDEX x ON projects (user_id, id);", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.MkdirAll(migrationsDir(root), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, root, map[string]string{"api/db/migrations/00009_key.sql": "-- +goose Up\n" + tc.migration + "\n"})
			got, err := hasOwnerKey(root, "projects", tc.body)
			if err != nil || got != tc.want {
				t.Fatalf("hasOwnerKey = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}
