package resource

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

// webApp writes a new app with the web pack, and a lock that lists its packs.
func webApp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	packs := []string{"base", "api", "web"}
	app := testutil.RenderPack(t, packs, map[string]any{"app_name": "todo", "module": "example.com/todo"})
	if err := os.CopyFS(dir, app); err != nil {
		t.Fatalf("write app: %v", err)
	}
	writeLock(t, dir, packs)
	return dir
}

func writeLock(t *testing.T, root string, packs []string) {
	t.Helper()
	lock := manifest.New()
	lock.Packs = packs
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
}

var todoPages = []string{
	"web/src/routes/todos/+page.server.ts",
	"web/src/routes/todos/+page.svelte",
	"web/src/routes/todos/[id]/+page.server.ts",
	"web/src/routes/todos/[id]/+page.svelte",
	"web/src/routes/todos/fields.svelte",
	"web/src/routes/todos/form.ts",
	"web/src/routes/todos/new/+page.server.ts",
	"web/src/routes/todos/new/+page.svelte",
}

func TestPagesPart(t *testing.T) {
	t.Parallel()
	root := webApp(t)
	out, err := run(context.Background(), gen.Input{Root: root, Args: goldenArgs, Flags: flagSet(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range todoPages {
		f, ok := out.Plan.Get(p)
		if !ok || f.Class != string(manifest.Scaffold) {
			t.Errorf("plan has %s: %v (class %q), want a scaffold file", p, ok, f.Class)
		}
	}
	if !slices.Contains(out.PostSteps, gen.PostClient) {
		t.Fatalf("post-steps %v, want the client post-step", out.PostSteps)
	}

	// --api-only skips the pages.
	out, err = run(context.Background(), gen.Input{Root: root, Args: goldenArgs, Flags: flagSet(t, "--api-only")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Plan.Get(todoPages[0]); ok || slices.Contains(out.PostSteps, gen.PostClient) {
		t.Fatalf("--api-only planned pages: %v %v", out.Plan.Paths(), out.PostSteps)
	}
}

func TestPagesNeedTheWebPack(t *testing.T) {
	t.Parallel()
	noLock := freshApp(t)
	apiOnly := freshApp(t)
	writeLock(t, apiOnly, []string{"base", "api"})
	badLock := freshApp(t)
	if err := os.WriteFile(filepath.Join(badLock, "spinup.lock"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, root := range map[string]string{"no lock": noLock, "api only": apiOnly, "bad lock": badLock} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spec, err := ParseSpec(root, []string{"todo", "title:string"}, "resource")
			if err != nil {
				t.Fatal(err)
			}
			plan, steps, err := planPages(context.Background(), spec)
			if name == "bad lock" {
				if err == nil {
					t.Fatal("a broken lock gave no error")
				}
				return
			}
			if err != nil || len(plan.Files) != 0 || len(steps) != 0 {
				t.Fatalf("plan %v, steps %v, err %v", plan.Paths(), steps, err)
			}
		})
	}
}

// TestPageInputs checks the input, the form conversion, and the list cell of
// every field type.
func TestPageInputs(t *testing.T) {
	t.Parallel()
	root := webApp(t)
	// The reference needs a parent table.
	write(t, root, map[string]string{"api/db/migrations/00002_create_people.sql": "-- +goose Up\nCREATE TABLE people (id bigint);\n"})
	tests := []struct {
		arg    string
		fields []string // in fields.svelte
		form   []string // in form.ts
		list   string   // in +page.svelte
	}{
		{"title:string", []string{`<Input id="title" name="title" type="text" value={values.title} required`}, []string{"title: string;", "title: values.title", "title: text(form, 'title')"}, "{item.title}"},
		{"body:text?", []string{`<textarea id="body" name="body" rows="4" value={values.body} class={control} aria-invalid`}, []string{"body: optional(values.body)", "body: item.body ?? ''"}, "{item.body ?? ''}"},
		{"done:bool", []string{`<input id="done" name="done" type="checkbox" checked={values.done}`}, []string{"done: boolean;", "done: form.has('done')", "done: false"}, "{item.done ? 'Yes' : 'No'}"},
		{"rank:int", []string{`type="number" value={values.rank} step="1" required`}, []string{"rank: integer(values.rank, 'rank', errors)", "rank: String(item.rank)"}, "{item.rank}"},
		{"size:bigint?", []string{`type="number" value={values.size} step="1" aria-invalid`}, []string{"size: values.size.trim() === '' ? null : integer(values.size, 'size', errors)", "size: item.size === null ? '' : String(item.size)"}, "{item.size ?? ''}"},
		{"ratio:float", []string{`type="number" value={values.ratio} step="any"`}, []string{"ratio: number(values.ratio, 'ratio', errors)", "function number("}, "{item.ratio}"},
		{"price:decimal", []string{`type="text" value={values.price} inputmode="decimal"`}, []string{"price: values.price"}, "{item.price}"},
		{"at:time", []string{`type="datetime-local" value={values.at}`, "UTC"}, []string{"at: time(values.at, 'at', errors)", "at: localTime(item.at)", "function localTime("}, "{item.at}"},
		{"due:date?", []string{`type="date" value={values.due}`}, []string{"due: optional(values.due)", "due: item.due ?? ''"}, "{item.due ?? ''}"},
		{"key:uuid", []string{`type="text" value={values.key} required`}, []string{"key: values.key"}, "{item.key}"},
		{"data:json", []string{`<textarea id="data" name="data" rows="4" value={values.data} class={[control, 'font-mono']}`}, []string{"data: object(values.data, 'data', errors)", "data: '{}'", "JSON.stringify(item.data, null, 2)"}, "{JSON.stringify(item.data)}"},
		{"owner:ref:person", []string{`<Label for="owner_id">Owner ID</Label>`, `<Input id="owner_id" name="owner_id" type="number" value={values.owner_id} step="1" required`}, []string{"owner_id: integer(values.owner_id, 'owner_id', errors)"}, "{item.owner_id}"},
		{"state:enum:draft|in_review", []string{`<select id="state" name="state" class={control} required`, `<option value="in_review" selected={values.state === 'in_review'}>in review</option>`}, []string{"state: values.state as Input['state']", "state: 'draft'"}, "{item.state}"},
		{"kind:enum:a|b?", []string{`<option value="" selected={values.kind === ''}>None</option>`}, []string{"kind: (values.kind === '' ? null : values.kind) as Input['kind']", "kind: ''"}, "{item.kind ?? ''}"},
	}
	for _, tc := range tests {
		t.Run(tc.arg, func(t *testing.T) {
			t.Parallel()
			spec, err := ParseSpec(root, []string{"todo", tc.arg}, "resource")
			if err != nil {
				t.Fatal(err)
			}
			plan, _, err := planPages(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			check := func(path string, want ...string) {
				f, ok := plan.Get(path)
				if !ok {
					t.Fatalf("plan has no %s", path)
				}
				for _, w := range want {
					if !strings.Contains(string(f.Data), w) {
						t.Errorf("%s does not contain %q:\n%s", path, w, f.Data)
					}
				}
			}
			check("web/src/routes/todos/fields.svelte", tc.fields...)
			check("web/src/routes/todos/form.ts", tc.form...)
			check("web/src/routes/todos/+page.svelte", tc.list)
		})
	}
}

func TestPagesHelpersOnlyWhenUsed(t *testing.T) {
	t.Parallel()
	root := webApp(t)
	spec, err := ParseSpec(root, []string{"todo", "title:string", "done:bool"}, "resource")
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := planPages(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	form, _ := plan.Get("web/src/routes/todos/form.ts")
	for _, helper := range []string{"function optional(", "function integer(", "function number(", "function time(", "function object("} {
		if strings.Contains(string(form.Data), helper) {
			t.Errorf("form.ts has the unused %s", helper)
		}
	}
	fieldsFile, _ := plan.Get("web/src/routes/todos/fields.svelte")
	if strings.Contains(string(fieldsFile.Data), "const control") {
		t.Error("fields.svelte has the unused control class")
	}
}

func TestPagePaths(t *testing.T) {
	t.Parallel()
	name, err := fields.ParseName("line_item")
	if err != nil {
		t.Fatal(err)
	}
	got := pagePaths(name)
	if len(got) != len(pageFiles) || got[0] != "web/src/routes/line-items/+page.server.ts" {
		t.Fatalf("pagePaths = %v", got)
	}
	all := Paths(name)
	for _, p := range got {
		if !slices.Contains(all, p) {
			t.Errorf("Paths() has no %s", p)
		}
	}
	todo, _ := fields.ParseName("todo")
	if !reflect.DeepEqual(pagePaths(todo), todoPages) {
		t.Fatalf("pagePaths(todo) = %v", pagePaths(todo))
	}
}

func TestPageNames(t *testing.T) {
	t.Parallel()
	root := webApp(t)
	spec, err := ParseSpec(root, []string{"line_item", "name:string"}, "resource")
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := planPages(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"web/src/routes/line-items/+page.server.ts":      "api.GET('/api/line_items', {",
		"web/src/routes/line-items/+page.svelte":         `<Button href="/line-items/new">New line item</Button>`,
		"web/src/routes/line-items/[id]/+page.server.ts": "redirect(303, '/line-items');",
		"web/src/routes/line-items/[id]/+page.svelte":    "<Card.Title>Line item {data.item.id}</Card.Title>",
		"web/src/routes/line-items/form.ts":              "components['schemas']['LineItemInput']",
		"web/src/routes/line-items/new/+page.server.ts":  "redirect(303, '/line-items/' + data.id);",
	} {
		f, ok := plan.Get(path)
		if !ok || !strings.Contains(string(f.Data), want) {
			t.Errorf("%s does not contain %q:\n%s", path, want, f.Data)
		}
	}
}
