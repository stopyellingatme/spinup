package resource

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// freshApp writes a new API app (packs base and api) to a temporary directory.
func freshApp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	app := testutil.RenderPack(t, []string{"base", "api"}, map[string]any{"app_name": "todo", "module": "example.com/todo"})
	if err := os.CopyFS(dir, app); err != nil {
		t.Fatalf("write app: %v", err)
	}
	return dir
}

func flagSet(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("resource", pflag.ContinueOnError)
	fs.Bool("api-only", false, "")
	fs.Bool("no-tests", false, "")
	fs.Bool("force", false, "")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return fs
}

// write adds files to the app at root.
func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

var goldenArgs = []string{"todo", "title:string", "done:bool", "due:date?", "priority:enum:low|normal|high"}

func TestGoldenGenResource(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "gen-resource")
	root := freshApp(t)
	render := func() (fs.FS, error) {
		out, err := run(context.Background(), gen.Input{Root: root, Args: goldenArgs, Flags: flagSet(t)})
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
	testutil.Golden(t, "gen-resource", got)
	testutil.Deterministic(t, render)
}

func TestRunPlan(t *testing.T) {
	t.Parallel()
	root := freshApp(t)
	out, err := run(context.Background(), gen.Input{Root: root, Args: goldenArgs, Flags: flagSet(t)})
	if err != nil {
		t.Fatal(err)
	}
	classes := map[string]string{}
	for _, f := range out.Plan.Files {
		classes[f.Path] = f.Class
	}
	want := map[string]string{
		"api/db/migrations/00002_create_todos.sql": "seed",
		"api/db/queries/todos.sql":                 "scaffold",
		"api/openapi/paths/todos.yaml":             "scaffold",
		"api/openapi/schemas/todo.yaml":            "scaffold",
		"api/internal/handlers/server.go":          "scaffold",
		"api/internal/handlers/todos.go":           "scaffold",
		"api/internal/handlers/todos_test.go":      "scaffold",
		"api/internal/routes/routes_gen.go":        "managed",
	}
	if !reflect.DeepEqual(classes, want) {
		t.Fatalf("plan classes:\n got %v\nwant %v", classes, want)
	}
	if want := []gen.PostStep{gen.PostOpenAPI, gen.PostSQLC, gen.PostGofmt}; !reflect.DeepEqual(out.PostSteps, want) {
		t.Fatalf("post-steps %v, want %v", out.PostSteps, want)
	}
	if want := []string{"spinup db migrate up", "cd api && go test ./..."}; !reflect.DeepEqual(out.Next, want) {
		t.Fatalf("next %v, want %v", out.Next, want)
	}

	// --no-tests drops the test, and an existing server.go stays as it is.
	write(t, root, map[string]string{serverPath: "package handlers\n"})
	out, err = run(context.Background(), gen.Input{Root: root, Args: goldenArgs, Flags: flagSet(t, "--no-tests")})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{serverPath, handlersTestPath(fields.Name{Singular: "todo", Plural: "todos"})} {
		if _, ok := out.Plan.Get(p); ok {
			t.Errorf("plan has %s, want it left out", p)
		}
	}
}

func TestRunRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files map[string]string
		args  []string
		want  string
	}{
		{"no fields", nil, []string{"todo"}, "at least one field"},
		{"bad field", nil, []string{"todo", "title"}, "name:type"},
		{"missing parent", nil, []string{"todo", "owner:ref:member"}, "no migration in api/db/migrations creates it"},
		{"required self reference", nil, []string{"category", "parent:ref:category"}, "Try: use parent:ref:category?"},
		{"table exists", map[string]string{"api/db/migrations/00002_todos.sql": "-- +goose Up\ncreate table todos (id bigint);\n"}, []string{"todo", "title:string"}, "Try: pass --force"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := freshApp(t)
			write(t, root, tc.files)
			_, err := run(context.Background(), gen.Input{Root: root, Args: tc.args, Flags: flagSet(t)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error %q has no Try: line", err)
			}
		})
	}
}

func TestForceReusesMigration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, want string
	}{
		{"generated migration", "00002_create_todos.sql", "api/db/migrations/00002_create_todos.sql"},
		{"other migration", "00002_schema.sql", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := freshApp(t)
			write(t, root, map[string]string{
				"api/db/migrations/" + tc.file:          "-- +goose Up\nCREATE TABLE todos (id bigint);\n",
				"api/db/migrations/00003_add_index.sql": "-- +goose Up\n",
			})
			out, err := run(context.Background(), gen.Input{Root: root, Args: []string{"todo", "title:string"}, Flags: flagSet(t, "--force")})
			if err != nil {
				t.Fatal(err)
			}
			var migrations []string
			for _, p := range out.Plan.Paths() {
				if strings.HasPrefix(p, "api/db/migrations/") {
					migrations = append(migrations, p)
				}
			}
			var want []string
			if tc.want != "" {
				want = []string{tc.want}
			}
			if !reflect.DeepEqual(migrations, want) {
				t.Fatalf("migrations %v, want %v", migrations, want)
			}
		})
	}
}

func TestParts(t *testing.T) {
	t.Parallel()
	root := freshApp(t)
	page := func(path string) Part {
		return Part{Name: "page", Plan: func(_ context.Context, spec Spec) (vfs.Plan, []gen.PostStep, error) {
			if spec.Name.Plural != "todos" || len(spec.Fields) != 1 || spec.Root != root {
				t.Errorf("part spec %+v", spec)
			}
			var p vfs.Plan
			err := p.Add(vfs.File{Path: path, Data: []byte("page\n"), Class: "scaffold"})
			return p, []gen.PostStep{"web"}, err
		}}
	}
	in := gen.Input{Root: root, Args: []string{"todo", "title:string"}, Flags: flagSet(t)}
	out, err := build(context.Background(), in, []Part{page("web/src/routes/todos/+page.svelte")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Plan.Get("web/src/routes/todos/+page.svelte"); !ok || out.PostSteps[len(out.PostSteps)-1] != "web" {
		t.Fatalf("part files or steps missing: %v %v", out.Plan.Paths(), out.PostSteps)
	}
	if _, err := build(context.Background(), in, []Part{page("api/db/queries/todos.sql")}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("duplicate path: error %v, want a conflict", err)
	}
	in.Flags = flagSet(t, "--api-only")
	out, err = build(context.Background(), in, []Part{page("web/x")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Plan.Get("web/x"); ok {
		t.Fatal("--api-only ran a registered part")
	}
}

func TestPaths(t *testing.T) {
	t.Parallel()
	name, err := fields.ParseName("line_item")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"api/db/queries/line_items.sql",
		"api/internal/handlers/line_items.go",
		"api/internal/handlers/line_items_test.go",
		"api/openapi/paths/line_items.yaml",
		"api/openapi/schemas/line_item.yaml",
	}
	got := Paths(name)
	// Registered parts may add paths. The built-in paths are always there.
	for _, p := range want {
		if !strings.Contains(strings.Join(got, "\n"), p) {
			t.Fatalf("Paths() = %v, want it to contain %s", got, p)
		}
	}
}

func TestNames(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, api, store string }{
		{"title", "Title", "Title"},
		{"owner_id", "OwnerId", "OwnerID"},
		{"address2_line", "Address2Line", "Address2Line"},
		{"id", "Id", "ID"},
	}
	for _, tc := range tests {
		if got := apiName(tc.in); got != tc.api {
			t.Errorf("apiName(%q) = %q, want %q", tc.in, got, tc.api)
		}
		if got := storeName(tc.in); got != tc.store {
			t.Errorf("storeName(%q) = %q, want %q", tc.in, got, tc.store)
		}
	}
	structs := map[string]string{
		"todos": "Todo", "people": "Person", "line_items": "LineItem", "categories": "Category",
		"statuses": "Status", "boxes": "Box", "mice": "Mouse", "women": "Woman", "children": "Child",
		"fish": "Fish", "campus": "Campus", "quizzes": "Quiz", "knives": "Knife", "wolves": "Wolf",
	}
	for table, want := range structs {
		if got := storeStruct(table); got != want {
			t.Errorf("storeStruct(%q) = %q, want %q", table, got, want)
		}
	}
}

func TestRequiredColumns(t *testing.T) {
	t.Parallel()
	body := `
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL,
    size text NOT NULL CHECK (size IN ('s', 'm')),
    note text,
    org_id bigint NOT NULL REFERENCES orgs (id),
    amount numeric(10, 2) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT x UNIQUE (name)`
	got := requiredColumns(body)
	want := []requiredColumn{
		{Name: "name", Value: "'sample'"},
		{Name: "size", Value: "'s'"},
		{Name: "org_id", Ref: "orgs"},
		{Name: "amount", Value: "1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requiredColumns:\n got %+v\nwant %+v", got, want)
	}
}

func TestParentInserts(t *testing.T) {
	t.Parallel()
	root := freshApp(t)
	write(t, root, map[string]string{
		"api/db/migrations/00002_create_orgs.sql":    "-- +goose Up\nCREATE TABLE orgs (\n    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,\n    name text NOT NULL\n);\n-- +goose Down\nCREATE TABLE members (id bigint);\n",
		"api/db/migrations/00003_create_members.sql": "-- +goose Up\nCREATE TABLE members (\n    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,\n    org_id bigint NOT NULL REFERENCES orgs (id),\n    active boolean NOT NULL\n);\n",
	})
	spec, err := ParseSpec(root, []string{"todo", "owner:ref:member", "parent:ref:todo?"}, "resource")
	if err != nil {
		t.Fatal(err)
	}
	got, err := parentInserts(root, spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []parentInsert{
		{Table: "orgs", SQL: "INSERT INTO orgs (name) VALUES ('sample')"},
		{Table: "members", SQL: "INSERT INTO members (org_id, active) VALUES ((SELECT max(id) FROM orgs), true)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parentInserts:\n got %+v\nwant %+v", got, want)
	}
}

// TestEveryFieldType checks that the Go code for each field type, nullable
// or not, and for one field or many, is valid Go. The script
// gen_resource.txtar and the app gate compile and run it.
func TestEveryFieldType(t *testing.T) {
	t.Parallel()
	var all []string
	for _, typ := range []string{"string", "text", "int", "bigint", "bool", "float", "decimal", "time", "date", "uuid", "json", "enum:a|b"} {
		name := "f_" + strings.NewReplacer(":", "_", "|", "_").Replace(typ)
		all = append(all, name+":"+typ, name+"_null:"+typ+"?")
	}
	root := freshApp(t)
	cases := [][]string{append([]string{"gadget"}, all...)}
	for _, arg := range all {
		cases = append(cases, []string{"gadget", arg})
	}
	for _, args := range cases {
		out, err := run(context.Background(), gen.Input{Root: root, Args: args, Flags: flagSet(t)})
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if len(out.Plan.Files) != 8 {
			t.Fatalf("%v: %d files, want 8", args, len(out.Plan.Files))
		}
	}
}
