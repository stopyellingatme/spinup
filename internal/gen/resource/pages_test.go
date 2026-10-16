package resource

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// webApp writes a new app with the web pack, and a lock that lists its packs.
func webApp(t *testing.T) string {
	t.Helper()
	return renderApp(t, []string{"base", "api", "web"}, nil)
}

// authApp writes a new app with the web pack and the auth module.
func authApp(t *testing.T) string {
	t.Helper()
	return renderApp(t, []string{"base", "api", "web", "auth"}, []string{"auth"})
}

func renderApp(t *testing.T, packs, modules []string) string {
	t.Helper()
	dir := t.TempDir()
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo"}
	if modules != nil {
		answers["modules"] = modules
	}
	if err := os.CopyFS(dir, testutil.RenderPack(t, packs, answers)); err != nil {
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
	"web/src/routes/todos/[id]/edit/+page.server.ts",
	"web/src/routes/todos/[id]/edit/+page.svelte",
	"web/src/routes/todos/fields.svelte",
	"web/src/routes/todos/new/+page.server.ts",
	"web/src/routes/todos/new/+page.svelte",
	"web/src/routes/todos/schema.ts",
}

// planTodo plans the pages of a todo with args as its fields.
func planTodo(t *testing.T, root string, args ...string) vfs.Plan {
	t.Helper()
	spec, err := ParseSpec(root, append([]string{"todo"}, args...), "resource")
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := planPages(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// file returns the text of a planned page file of the todos.
func file(t *testing.T, plan vfs.Plan, name string) string {
	t.Helper()
	f, ok := plan.Get("web/src/routes/todos/" + name)
	if !ok {
		t.Fatalf("plan has no %s: %v", name, plan.Paths())
	}
	return string(f.Data)
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
	if _, ok := out.Plan.Get("web/src/routes/todos/form.ts"); ok {
		t.Error("plan has form.ts, which schema.ts replaces")
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

// TestPagesOldApp checks that an app without the M12 web pack files gets an
// error that names each missing file and package.
func TestPagesOldApp(t *testing.T) {
	t.Parallel()
	const try = "\nTry: spinup upgrade (it adds the new web pack files), then run the generator again"
	tests := []struct {
		name   string
		change func(t *testing.T, root string)
		want   []string
	}{
		{"component", func(t *testing.T, root string) {
			if err := os.RemoveAll(filepath.Join(root, "web/src/lib/components/ui/table")); err != nil {
				t.Fatal(err)
			}
		}, []string{"web/src/lib/components/ui/table/"}},
		{"field", func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "web/src/lib/components/app/field.svelte")); err != nil {
				t.Fatal(err)
			}
		}, []string{"web/src/lib/components/app/field.svelte"}},
		{"packages", func(t *testing.T, root string) {
			path := filepath.Join(root, "web/package.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var pkg map[string]any
			if err := json.Unmarshal(data, &pkg); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"dependencies", "devDependencies"} {
				deps, _ := pkg[key].(map[string]any)
				delete(deps, "zod")
				delete(deps, "svelte-sonner")
			}
			data, err = json.Marshal(pkg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}, []string{"the npm package zod", "the npm package svelte-sonner"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := webApp(t)
			tc.change(t, root)
			out, err := run(t.Context(), gen.Input{Root: root, Args: goldenArgs, Flags: flagSet(t)})
			if err == nil {
				t.Fatalf("planned pages for an old app: %v", out.Plan.Paths())
			}
			if !strings.HasSuffix(err.Error(), try) {
				t.Errorf("error %q does not end with %q", err, try)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
			// --api-only needs none of these files.
			if _, err := run(t.Context(), gen.Input{Root: root, Args: goldenArgs, Flags: flagSet(t, "--api-only")}); err != nil {
				t.Fatalf("--api-only: %v", err)
			}
		})
	}
}

// TestPageInputs checks the input, the schema, the conversions, and the list
// cell of every field type.
func TestPageInputs(t *testing.T) {
	t.Parallel()
	root := webApp(t)
	// The reference needs a parent table.
	write(t, root, map[string]string{"api/db/migrations/00002_create_people.sql": "-- +goose Up\nCREATE TABLE people (id bigint);\n"})
	tests := []struct {
		arg    string
		fields []string // in fields.svelte
		schema []string // in schema.ts
		list   string   // in +page.svelte, or empty when the list does not show the field
	}{
		{"name:string", []string{`<Field label="Name" name="name" errors={$errors.name}>`, `<Input {...props} bind:value={$values.name} required />`}, []string{"name: z.string().trim().min(1, 'Name is required.')", "name: item.name", "name: values.name"}, "{itemTitle(item)}"},
		{"body:text?", []string{`<textarea {...props} rows="4" bind:value={$values.body} class={[control, 'min-h-24']}></textarea>`}, []string{"body: z.string().trim()", "body: item.body ?? ''", "body: values.body === '' ? null : values.body"}, ""},
		{"done:bool", []string{`<Checkbox {...props} bind:checked={$values.done} />`}, []string{"done: z.boolean()", "done: item.done", "done: values.done"}, `<Checkbox`},
		{"flag:bool?", []string{`<select {...props} bind:value={$values.flag} class={[control, 'h-8']}>`, `<option value="">Not set</option>`, `<option value="true">Yes</option>`, `<option value="false">No</option>`}, []string{"flag: z.enum(['', 'true', 'false']", "flag: item.flag === null ? '' : item.flag ? 'true' : 'false'", "flag: values.flag === '' ? null : values.flag === 'true'"}, "{#if item.flag === null}—{:else if item.flag}<CheckIcon"},
		{"rank:int", []string{`<Input {...props} type="number" step="1" bind:value={$values.rank} required />`}, []string{"rank: z.number({ error: 'Enter a whole number.' }).int('Enter a whole number.')", "rank: item.rank", "rank: values.rank"}, "{item.rank}"},
		{"size:bigint?", []string{`<Input {...props} type="number" step="1" bind:value={$values.size} />`}, []string{".int('Enter a whole number.').nullable()", "size: item.size"}, "{item.size ?? '—'}"},
		{"ratio:float", []string{`type="number" step="any" bind:value={$values.ratio} required`}, []string{"ratio: z.number({ error: 'Enter a number.' })"}, "{item.ratio}"},
		{"price:decimal", []string{`<Input {...props} inputmode="decimal" bind:value={$values.price} required />`}, []string{`price: z.string().trim().regex(/^-?\d+(\.\d+)?$/`, "price: values.price"}, "{item.price}"},
		{"at:time", []string{`<Field label="At" name="at" errors={$errors.at} description="In UTC.">`, `type="datetime-local" step="1" bind:value={$values.at} required`}, []string{"at: localTime(item.at)", "at: time(values.at, original?.at)", "function localTime(", "export function toBody(values: Values, original?: Item): Input"}, "{formatTime(item.at)}"},
		{"due:date?", []string{`<Input {...props} type="date" bind:value={$values.due} />`}, []string{`due: z.string().trim().regex(/^(\d{4}-\d{2}-\d{2})?$/, 'Enter a date.')`, "due: item.due ?? ''", "due: values.due === '' ? null : values.due", "timeZone: 'UTC'"}, "{item.due ? formatDate(item.due) : '—'}"},
		{"key:uuid", []string{`<Input {...props} autocomplete="off" spellcheck="false" bind:value={$values.key} required />`}, []string{"key: values.key", "/i, 'Enter a UUID"}, "{item.key}"},
		{"data:json", []string{`<textarea {...props} rows="6" spellcheck="false" bind:value={$values.data} class={[control, 'min-h-24 font-mono']} required></textarea>`}, []string{"data: z.string().refine(isObject,", ".default('{}')", "data: JSON.stringify(item.data, null, 2)", "data: JSON.parse(values.data) as Record<string, unknown>", "function isObject("}, ""},
		{"owner:ref:person", []string{`<Field label="Owner ID" name="owner_id" errors={$errors.owner_id}>`, `<Input {...props} type="number" step="1" bind:value={$values.owner_id} required />`}, []string{"owner_id: z.number("}, "{item.owner_id}"},
		{"state:enum:draft|in_review", []string{`<select {...props} bind:value={$values.state} class={[control, 'h-8']} required>`, "{#each Object.entries(stateLabels) as [value, label] (value)}"}, []string{"state: z.enum(['draft', 'in_review'], { error: 'Choose the state.' })", "export const stateLabels: Record<NonNullable<Item['state']>, string> = {\n\tdraft: 'Draft',\n\tin_review: 'In review'\n};"}, `<Badge variant="secondary">{stateLabels[item.state]}</Badge>`},
		{"kind:enum:a|b?", []string{`<option value="">None</option>`}, []string{"kind: z.enum(['', 'a', 'b']", "kind: item.kind ?? ''", "kind: values.kind === '' ? null : values.kind"}, "{#if item.kind}<Badge"},
		{"tier:enum:1st|2nd", nil, []string{"\t'1st': '1st',\n\t'2nd': '2nd'\n"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.arg, func(t *testing.T) {
			t.Parallel()
			plan := planTodo(t, root, tc.arg)
			check := func(name string, want ...string) {
				data := file(t, plan, name)
				for _, w := range want {
					if !strings.Contains(data, w) {
						t.Errorf("%s does not contain %q:\n%s", name, w, data)
					}
				}
			}
			check("fields.svelte", tc.fields...)
			check("schema.ts", tc.schema...)
			if tc.list != "" {
				check("+page.svelte", tc.list)
			}
		})
	}
}

func TestPagesHelpersOnlyWhenUsed(t *testing.T) {
	t.Parallel()
	root := webApp(t)
	plan := planTodo(t, root, "title:string", "done:bool")
	schema := file(t, plan, "schema.ts")
	for _, helper := range []string{"function localTime(", "function time(", "function isObject(", "function formatDate(", "Labels"} {
		if strings.Contains(schema, helper) {
			t.Errorf("schema.ts has the unused %s", helper)
		}
	}
	if fields := file(t, plan, "fields.svelte"); strings.Contains(fields, "const control") {
		t.Error("fields.svelte has the unused control class")
	}
	list := file(t, plan, "+page.svelte")
	for _, unused := range []string{"CheckIcon", "Badge", "formatDate"} {
		if strings.Contains(list, unused) {
			t.Errorf("the list has the unused %s", unused)
		}
	}
}

// TestPageList checks the list: the title link, the inline toggle of the first
// non-null bool, the row menu, the pagination, and the delete dialog.
func TestPageList(t *testing.T) {
	t.Parallel()
	root := webApp(t)
	plan := planTodo(t, root, "flag:bool?", "rank:int", "done:bool", "title:string", "other:bool", "label:string")
	list, server := file(t, plan, "+page.svelte"), file(t, plan, "+page.server.ts")
	for _, want := range []string{
		`<a href="/todos/{item.id}" class="font-medium underline-offset-4 hover:underline">`,
		`<Table.Head>Title</Table.Head>`,
		`action="?/toggle"`,
		`<input type="hidden" name="value" value={String(!item.done)} />`,
		`checked={pending[item.id] ?? item.done}`,
		`await update({ reset: false, navigate: false });`,
		`<Table.Head>Other</Table.Head>`,
		`<Table.Head>Label</Table.Head>`,
		`<a {...props} href="/todos/{item.id}/edit">`,
		`onSelect={() => confirmDelete(item)}`,
		`<AlertDialog.Root bind:open={confirming}>`,
		`action="?/delete&page={data.page}"`,
		`{#if data.total > data.perPage}`,
		"`?page=${n}`",
		`toast.success('Todo deleted.');`,
		`<Button href="/todos/new">`,
		`<h2 class="font-medium">No todos yet</h2>`,
	} {
		if !strings.Contains(list, want) {
			t.Errorf("the list does not contain %q", want)
		}
	}
	if strings.Contains(list, `<Table.Head>Done</Table.Head>`) || strings.Contains(list, "String(!item.other)") {
		t.Error("the list shows the toggle as a column, or toggles the second bool")
	}
	for _, want := range []string{
		"const perPage = 20;",
		"params: { query: { limit: perPage, offset: (page - 1) * perPage } }",
		"body: { ...toBody(fromItem(item)), done: form.get('value') === 'true' }",
		"redirect(303, page > 1 ? `/todos?page=${page}&deleted=1` : '/todos?deleted=1');",
	} {
		if !strings.Contains(server, want) {
			t.Errorf("the list server does not contain %q", want)
		}
	}

	// Without a string field, the link names the item by its id. Without a non-null bool, no toggle.
	plan = planTodo(t, root, "rank:int", "flag:bool?")
	list, server, schema := file(t, plan, "+page.svelte"), file(t, plan, "+page.server.ts"), file(t, plan, "schema.ts")
	if !strings.Contains(schema, "return `Todo ${item.id}`;") || !strings.Contains(list, "<Table.Head>Todo</Table.Head>") {
		t.Errorf("no id title:\n%s", schema)
	}
	if strings.Contains(list, "toggle") || strings.Contains(server, "toggle") || strings.Contains(list, "Checkbox") {
		t.Error("a resource without a non-null bool has a toggle")
	}
}

// TestPagesAuth checks the guards: with the auth module, every load and action
// starts with requireUser. Without it, no file imports the auth helpers.
func TestPagesAuth(t *testing.T) {
	t.Parallel()
	head := regexp.MustCompile(`(?m)^(export const load: PageServerLoad = async \(.*\) => \{|\t(default|toggle|delete): async \(.*\) => \{)\n(.*)$`)
	for _, auth := range []bool{false, true} {
		t.Run(map[bool]string{false: "no auth", true: "auth"}[auth], func(t *testing.T) {
			t.Parallel()
			root := webApp(t)
			if auth {
				root = authApp(t)
			}
			out, err := run(t.Context(), gen.Input{Root: root, Args: goldenArgs, Flags: flagSet(t)})
			if err != nil {
				t.Fatal(err)
			}
			heads := 0
			for _, path := range todoPages {
				f, _ := out.Plan.Get(path)
				data := string(f.Data)
				if !auth {
					if strings.Contains(data, "server/auth") || strings.Contains(data, "requireUser") {
						t.Errorf("%s uses the auth helpers in an app without auth", path)
					}
					continue
				}
				if !strings.HasSuffix(path, ".server.ts") {
					continue
				}
				if !strings.Contains(data, "import { requireUser } from '#lib/server/auth.js';") {
					t.Errorf("%s does not import requireUser", path)
				}
				for _, m := range head.FindAllStringSubmatch(data, -1) {
					heads++
					if strings.TrimSpace(m[3]) != "requireUser(locals, url);" {
						t.Errorf("%s: %q starts with %q, want requireUser", path, m[1], m[3])
					}
				}
			}
			// Loads: list, read, edit, new. Actions: toggle, delete (list), delete (read), edit, new.
			if auth && heads != 9 {
				t.Errorf("found %d loads and actions, want 9", heads)
			}
		})
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
		"web/src/routes/line-items/+page.server.ts":           "api.GET('/api/line_items', {",
		"web/src/routes/line-items/+page.svelte":              "New line item",
		"web/src/routes/line-items/[id]/+page.server.ts":      "redirect(303, '/line-items?deleted=1');",
		"web/src/routes/line-items/[id]/+page.svelte":         `<Button href="/line-items/{item.id}/edit" variant="outline">`,
		"web/src/routes/line-items/[id]/edit/+page.server.ts": "redirect(303, `/line-items/${data.id}?saved=1`);",
		"web/src/routes/line-items/[id]/edit/+page.svelte":    `<PageHeader title="Edit line item"`,
		"web/src/routes/line-items/schema.ts":                 "components['schemas']['LineItemInput']",
		"web/src/routes/line-items/new/+page.server.ts":       "redirect(303, `/line-items/${data.id}?created=1`);",
		"web/src/routes/line-items/new/+page.svelte":          "Create line item",
	} {
		f, ok := plan.Get(path)
		if !ok || !strings.Contains(string(f.Data), want) {
			t.Errorf("%s does not contain %q:\n%s", path, want, f.Data)
		}
	}
}

// TestPageSchemaRoundTrip runs schema.ts in Node with the zod version of the web
// pack: the form values of an item pass the schema and convert back to the
// same API body, and invalid values get their messages.
func TestPageSchemaRoundTrip(t *testing.T) {
	t.Parallel()
	node, err := runexec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	pnpm, err := runexec.LookPath("pnpm")
	if err != nil {
		t.Skip("pnpm is not on PATH: put scripts/bin on PATH (the Makefile does)")
	}
	root := webApp(t)
	plan := planTodo(t, root, "title:string", "done:bool", "count:int", "size:bigint?", "ratio:float", "price:decimal?",
		"at:time", "maybe:time?", "day:date", "due:date?", "key:uuid?", "data:json", "meta:json?", "flag:bool?",
		"state:enum:draft|in_review", "kind:enum:a|b?", "body:text?")

	data, err := os.ReadFile(filepath.Join(root, "web", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil || pkg.Dependencies["zod"] == "" {
		t.Fatalf("web/package.json has no zod: %v", err)
	}
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"package.json": `{"name":"schema-test","private":true,"type":"module","dependencies":{"zod":"` + pkg.Dependencies["zod"] + `"}}`,
		"schema.ts":    file(t, plan, "schema.ts"),
		"test.ts":      schemaRoundTrip,
	})
	runner := runexec.OSRunner{}
	if result, err := runner.Run(t.Context(), runexec.Cmd{Name: pnpm, Args: []string{"install", "--offline", "--silent"}, Dir: dir}); err != nil {
		t.Fatalf("install zod offline: %v\n%s", err, result.Stderr)
	}
	if result, err := runner.Run(t.Context(), runexec.Cmd{Name: node, Args: []string{"test.ts"}, Dir: dir}); err != nil {
		t.Fatalf("schema round trip: %v\n%s", err, result.Stderr)
	}
}

const schemaRoundTrip = `import assert from 'node:assert/strict';
import { fromItem, itemTitle, problemMessage, schema, toBody } from './schema.ts';

const base = {
	id: 7, title: 'old', done: true, count: 3, size: null, ratio: 1.5, price: null,
	at: '2026-01-02T03:04:56.789123Z', maybe: null, day: '2026-10-16', due: null, key: null,
	data: { a: 1 }, meta: null, flag: null, state: 'in_review', kind: null, body: null,
	created_at: '2026-01-02T03:04:56Z', updated_at: '2026-01-02T03:04:56Z'
};
const items = [
	base,
	{
		...base, size: 9, price: '12.50', maybe: '2026-01-02T04:04:56.789+01:00', due: '2026-12-31',
		key: '123e4567-e89b-12d3-a456-426614174000', meta: { b: [1] }, flag: false, kind: 'b', body: 'text'
	},
	{ ...base, done: false, at: '2026-01-02T03:04:00.789123Z', flag: true }
];
for (const item of items) {
	const values = fromItem(item);
	// Browsers omit zero seconds from datetime-local values.
	values.at = values.at.replace(/:00$/, '');
	const parsed = schema.safeParse(values);
	assert.ok(parsed.success, JSON.stringify(parsed.error?.issues));
	const { id, created_at, updated_at, ...input } = item;
	assert.deepEqual(toBody(parsed.data, item), input);
}

// A changed time is a new UTC instant. Empty optional inputs send null.
const changed = schema.parse({ ...fromItem(base), at: '2026-01-02T03:04:57', maybe: '', price: ' ', key: '', meta: ' ', kind: '' });
const body = toBody(changed, base);
assert.equal(body.at, '2026-01-02T03:04:57.000Z');
for (const name of ['maybe', 'price', 'key', 'meta', 'kind']) assert.equal(body[name], null, name);

// Invalid values get the messages of the schema.
const bad = schema.safeParse({
	...fromItem(base), title: '  ', count: 1.5, price: 'abc', at: 'x', day: '', key: 'nope', data: '[1]', state: 'other'
});
assert.ok(!bad.success);
assert.deepEqual(Object.fromEntries(bad.error.issues.map((i) => [i.path[0], i.message])), {
	title: 'Title is required.',
	count: 'Enter a whole number.',
	price: 'Enter a decimal number, for example 12.50.',
	at: 'Enter a date and time.',
	day: 'Enter a date.',
	key: 'Enter a UUID, for example 123e4567-e89b-12d3-a456-426614174000.',
	data: 'Enter a JSON object, for example {"key": "value"}.',
	state: 'Choose the state.'
});

assert.equal(itemTitle({ ...base, title: '' }), 'Todo 7');
assert.equal(problemMessage(undefined, 502), 'The API returned status 502.');
assert.equal(problemMessage({ type: 'about:blank', title: 'Conflict', status: 409, detail: 'Taken.' }, 409), 'Taken.');
`

func TestEmbedRefusesResourcePages(t *testing.T) {
	t.Parallel()
	root := webApp(t)
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lock.Answers["frontend_mode"] = "embed"
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	for _, apiOnly := range []bool{false, true} {
		args := []string{}
		if apiOnly {
			args = append(args, "--api-only")
		}
		out, err := run(t.Context(), gen.Input{Root: root, Args: goldenArgs, Flags: flagSet(t, args...)})
		if apiOnly {
			if err != nil || out == nil {
				t.Fatalf("API-only resource: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "Try:") || !strings.Contains(err.Error(), "embed") {
			t.Fatalf("resource pages: %v", err)
		}
	}
}
