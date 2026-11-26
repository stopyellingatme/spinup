package resource

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// embedTodoPages are the eleven browser scaffolds of a todo in embed mode.
var embedTodoPages = []string{
	"web/src/routes/todos/+page.svelte",
	"web/src/routes/todos/+page.ts",
	"web/src/routes/todos/[id]/+page.svelte",
	"web/src/routes/todos/[id]/+page.ts",
	"web/src/routes/todos/[id]/edit/+page.svelte",
	"web/src/routes/todos/[id]/edit/+page.ts",
	"web/src/routes/todos/fields.svelte",
	"web/src/routes/todos/form.ts",
	"web/src/routes/todos/new/+page.svelte",
	"web/src/routes/todos/new/+page.ts",
	"web/src/routes/todos/schema.ts",
}

// embedLoads are the browser loads of a todo, relative to the route folder.
var embedLoads = []string{"+page.ts", "[id]/+page.ts", "[id]/edit/+page.ts", "new/+page.ts"}

// embedApp writes a new embed app with the web pack and the given modules,
// and a lock that records the packs and the frontend mode. extra adds answers.
func embedApp(t *testing.T, modules []string, extra map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	packs := append([]string{"base", "api", "web"}, modules...)
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo", "frontend_mode": "embed"}
	if modules != nil {
		answers["modules"] = modules
	}
	for key, value := range extra {
		answers[key] = value
	}
	if err := os.CopyFS(dir, testutil.RenderPack(t, packs, answers)); err != nil {
		t.Fatalf("write app: %v", err)
	}
	lock := manifest.New()
	lock.Packs = packs
	lock.Answers["frontend_mode"] = "embed"
	if err := lock.Save(filepath.Join(dir, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// embedAuthApp writes an embed app with the auth module.
func embedAuthApp(t *testing.T) string {
	t.Helper()
	return embedApp(t, []string{"auth"}, nil)
}

// planSpec plans the pages of spec and fails the test on an error.
func planSpec(t *testing.T, spec Spec) vfs.Plan {
	t.Helper()
	plan, steps, err := planPages(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(steps, gen.PostClient) {
		t.Fatalf("post-steps %v, want the client post-step", steps)
	}
	return plan
}

// todoSpec parses a todo with args as its fields.
func todoSpec(t *testing.T, root string, args ...string) Spec {
	t.Helper()
	spec, err := ParseSpec(root, append([]string{"todo"}, args...), "resource")
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// TestEmbedPagesFamily checks the eleven browser scaffolds of each variant: no
// server file, no raw HTML, and no auth code without auth.
func TestEmbedPagesFamily(t *testing.T) {
	t.Parallel()
	for _, auth := range []bool{false, true} {
		t.Run(map[bool]string{false: "no auth", true: "auth"}[auth], func(t *testing.T) {
			t.Parallel()
			root := embedApp(t, nil, nil)
			if auth {
				root = embedAuthApp(t)
			}
			plan := planSpec(t, todoSpec(t, root, goldenArgs[1:]...))
			if !slices.Equal(plan.Paths(), embedTodoPages) {
				t.Fatalf("plan = %v\nwant %v", plan.Paths(), embedTodoPages)
			}
			for _, f := range plan.Files {
				data := string(f.Data)
				if f.Class != string(manifest.Scaffold) {
					t.Errorf("%s: class %q, want scaffold", f.Path, f.Class)
				}
				for _, banned := range []string{"{@html", "+page.server", "$app/forms", "use:enhance={", "__PUBLIC_ORIGIN__", "#lib/server/", "[[", "]]"} {
					if strings.Contains(data, banned) {
						t.Errorf("%s contains %q", f.Path, banned)
					}
				}
				if auth {
					continue
				}
				for _, banned := range []string{"#lib/auth", "session-watch", "observedFetch", "await parent()", "requireUser", "requireRole"} {
					if strings.Contains(data, banned) {
						t.Errorf("%s uses %q in an app without auth", f.Path, banned)
					}
				}
			}
			form := file(t, plan, "form.ts")
			for _, want := range []string{
				"export const RESOURCE_DEP = 'app:resource:todos';",
				"export const unreachable = 'The server did not answer. Check the connection and try again.';",
				"export const notFound = 'This todo does not exist.';",
				"export function id(value: string | null): number {",
				"export async function getItem(fetch: typeof globalThis.fetch, param: string): Promise<Item> {",
				"export function applyFailure(form: SuperValidated<Values>, problem: Problem | undefined, status: number): void {",
				"if (response.status === 404) error(404, notFound);",
			} {
				if !strings.Contains(form, want) {
					t.Errorf("form.ts does not contain %q:\n%s", want, form)
				}
			}
			observed := strings.Contains(form, "fetch: observedFetch(fetch)")
			if observed != auth {
				t.Errorf("getItem observes the fetch: %v, want %v", observed, auth)
			}
		})
	}
}

// TestEmbedPagesGuardBeforeFetch checks that each load with auth awaits the
// root load and runs the guard before it reads the API.
func TestEmbedPagesGuardBeforeFetch(t *testing.T) {
	t.Parallel()
	root := embedAuthApp(t)
	for _, roles := range [][]string{nil, {"manager", "staff"}} {
		spec := todoSpec(t, root, goldenArgs[1:]...)
		spec.Roles = roles
		plan := planSpec(t, spec)
		guard, other := "requireUser(user, url);", "requireRole("
		if roles != nil {
			guard, other = "requireRole(user, url, 'manager', 'staff');", "requireUser("
		}
		for _, name := range embedLoads {
			data := file(t, plan, name)
			parent := strings.Index(data, "const { user } = await parent();")
			at := strings.Index(data, guard)
			if parent < 0 || at < parent {
				t.Errorf("%s (roles %v): the guard %q does not follow await parent():\n%s", name, roles, guard, data)
			}
			if strings.Contains(data, other) {
				t.Errorf("%s (roles %v) contains %s", name, roles, other)
			}
			for _, read := range []string{"api.", "getItem(fetch"} {
				if i := strings.Index(data, read); i >= 0 && i < at {
					t.Errorf("%s (roles %v): %s comes before the guard", name, roles, read)
				}
			}
		}
		list := file(t, plan, "+page.ts")
		if !strings.Contains(list, "fetch: observedFetch(fetch),") || !strings.Contains(list, "depends(RESOURCE_DEP);") {
			t.Errorf("the list load does not observe its fetch or does not depend on RESOURCE_DEP:\n%s", list)
		}
		for _, name := range []string{"+page.svelte", "[id]/+page.svelte", "[id]/edit/+page.svelte", "new/+page.svelte"} {
			if !strings.Contains(file(t, plan, name), "observedFetch(fetch)") {
				t.Errorf("%s: the writes do not go through observedFetch", name)
			}
		}
	}
}

// TestEmbedPagesOwnership checks that the default owner, --shared, and --role
// give the same pages apart from the guard: the form never sends an owner id.
func TestEmbedPagesOwnership(t *testing.T) {
	t.Parallel()
	root := embedAuthApp(t)
	plans := map[string]vfs.Plan{}
	for _, args := range [][]string{nil, {"--shared"}} {
		out, err := run(t.Context(), gen.Input{Root: root, Args: goldenArgs, Flags: flagSet(t, args...)})
		if err != nil {
			t.Fatal(err)
		}
		var pages vfs.Plan
		for _, path := range embedTodoPages {
			f, ok := out.Plan.Get(path)
			if !ok {
				t.Fatalf("%v: plan has no %s: %v", args, path, out.Plan.Paths())
			}
			if err := pages.Add(f); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(f.Data), "user_id") {
				t.Errorf("%v: %s sends an owner id", args, path)
			}
		}
		for _, path := range todoPages {
			if _, ok := out.Plan.Get(path); ok && strings.HasSuffix(path, ".server.ts") {
				t.Errorf("%v: plan has the server file %s", args, path)
			}
		}
		nav, ok := out.Plan.Get(navPath)
		if !ok || !strings.Contains(string(nav.Data), "{ href: '/todos', label: 'Todos' },") || !strings.Contains(string(nav.Data), "export const home: string = '/todos';") {
			t.Errorf("%v: navigation = %s (planned %v)", args, nav.Data, ok)
		}
		plans[strings.Join(args, " ")] = pages
	}
	owned, shared := plans[""], plans["--shared"]
	for i, f := range owned.Files {
		if !bytes.Equal(f.Data, shared.Files[i].Data) {
			t.Errorf("%s differs between an owned and a shared resource", f.Path)
		}
	}
}

// TestEmbedPagesAPIOnly checks that --api-only plans no page and no client
// post-step in embed mode.
func TestEmbedPagesAPIOnly(t *testing.T) {
	t.Parallel()
	root := embedApp(t, nil, nil)
	out, err := run(t.Context(), gen.Input{Root: root, Args: goldenArgs, Flags: flagSet(t, "--api-only")})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range out.Plan.Paths() {
		if strings.HasPrefix(path, "web/") {
			t.Errorf("--api-only planned %s", path)
		}
	}
	if slices.Contains(out.PostSteps, gen.PostClient) {
		t.Errorf("--api-only requests the client post-step: %v", out.PostSteps)
	}
	if _, ok := out.Plan.Get("api/internal/handlers/todos.go"); !ok {
		t.Errorf("--api-only planned no handler: %v", out.Plan.Paths())
	}
}

// TestEmbedPagesFields checks that the browser family keeps the inputs and the
// conversions of node mode for each field type.
func TestEmbedPagesFields(t *testing.T) {
	t.Parallel()
	node, embed := webApp(t), embedApp(t, nil, nil)
	write(t, node, map[string]string{"api/db/migrations/00002_create_people.sql": "-- +goose Up\nCREATE TABLE people (id bigint);\n"})
	write(t, embed, map[string]string{"api/db/migrations/00002_create_people.sql": "-- +goose Up\nCREATE TABLE people (id bigint);\n"})
	args := []string{"title:string", "body:text?", "done:bool", "flag:bool?", "rank:int", "size:bigint?", "ratio:float",
		"price:decimal?", "at:time", "maybe:time?", "day:date", "due:date?", "key:uuid", "data:json", "meta:json?",
		"owner:ref:person", "state:enum:draft|in_review", "kind:enum:a|b?"}
	nodePlan, embedPlan := planSpec(t, todoSpec(t, node, args...)), planSpec(t, todoSpec(t, embed, args...))
	for _, name := range []string{"fields.svelte", "schema.ts"} {
		if file(t, nodePlan, name) != file(t, embedPlan, name) {
			t.Errorf("%s differs between the node and the embed family", name)
		}
	}
	list, item := file(t, embedPlan, "+page.svelte"), file(t, embedPlan, "[id]/+page.svelte")
	for _, want := range []string{
		"{item.due ? formatDate(item.due) : '—'}",
		"{formatTime(item.at)}",
		`<Badge variant="secondary">{stateLabels[item.state]}</Badge>`,
		"{#if item.flag === null}—{:else if item.flag}<svg",
		"import { stateLabels, kindLabels, formatDate, formatTime, fromItem, itemTitle, problemMessage, toBody, type Item } from './schema';",
	} {
		if !strings.Contains(list, want) {
			t.Errorf("the list does not contain %q", want)
		}
	}
	for _, want := range []string{
		`<dt class="text-muted-foreground text-sm font-medium">Owner ID</dt>`,
		`<dd class="whitespace-pre-wrap min-w-0 text-sm">{item.body ?? '—'}</dd>`,
		"{JSON.stringify(item.data, null, 2)}",
		"import { stateLabels, kindLabels, formatDate, formatTime, itemTitle, problemMessage } from '../schema';",
	} {
		if !strings.Contains(item, want) {
			t.Errorf("the read view does not contain %q", want)
		}
	}
	// A time field keeps the original value on a toggle and on an edit.
	if !strings.Contains(list, "body: { ...toBody(fromItem(current.data), current.data), done: value }") {
		t.Errorf("the toggle does not keep the original time:\n%s", list)
	}
	if edit := file(t, embedPlan, "[id]/edit/+page.svelte"); !strings.Contains(edit, "body: toBody(form.data, item)") {
		t.Errorf("the edit page does not keep the original time:\n%s", edit)
	}
}

// TestEmbedPagesToggle checks the toggle of the first non-null bool: it reads
// the current item, sends the full body with PUT, and clears the pending
// value in finally. A resource without a non-null bool has no toggle.
func TestEmbedPagesToggle(t *testing.T) {
	t.Parallel()
	root := embedAuthApp(t)
	plan := planSpec(t, todoSpec(t, root, "flag:bool?", "rank:int", "done:bool", "title:string", "other:bool"))
	list := file(t, plan, "+page.svelte")
	for _, want := range []string{
		"const current = await api.GET('/api/todos/{id}', { fetch: apiFetch, params: { path } });",
		"const saved = await api.PUT('/api/todos/{id}', {",
		"body: { ...toBody(fromItem(current.data)), done: value }",
		`<form method="POST" onsubmit={toggle} class="flex items-center">`,
		`aria-label="Done: {itemTitle(item)}"`,
		`aria-checked={pending[item.id] ?? item.done}`,
		"if (id in pending) return;",
		"} finally {\n\t\t\tdelete pending[id];\n\t\t}",
		"await invalidate(RESOURCE_DEP);",
		"toast.success('Todo deleted.');",
		"<Table.Head>Other</Table.Head>",
	} {
		if !strings.Contains(list, want) {
			t.Errorf("the list does not contain %q", want)
		}
	}
	if strings.Contains(list, "String(!item.other)") {
		t.Error("the list toggles the second bool")
	}
	plan = planSpec(t, todoSpec(t, root, "rank:int", "flag:bool?"))
	list = file(t, plan, "+page.svelte")
	if strings.Contains(list, "toggle") || strings.Contains(list, "pending") || strings.Contains(list, "toBody") {
		t.Errorf("a resource without a non-null bool has a toggle:\n%s", list)
	}
}

// TestEmbedPagesPendingFlags checks that each write catches a transport
// failure and clears its pending state, so a second attempt works.
func TestEmbedPagesPendingFlags(t *testing.T) {
	t.Parallel()
	plan := planSpec(t, todoSpec(t, embedApp(t, nil, nil), goldenArgs[1:]...))
	for name, wants := range map[string][]string{
		"+page.svelte":           {"} catch (err) {", "toast.error(unreachable);", "} finally {\n\t\t\tdeleting = false;\n\t\t\tconfirming = false;\n\t\t}", "} finally {\n\t\t\tdelete pending[id];\n\t\t}"},
		"[id]/+page.svelte":      {"failure = unreachable;", "} finally {\n\t\t\tif (deleting === id) deleting = null;\n\t\t\tif (id === data.item.id) confirming = false;\n\t\t}", "await goto('/todos', { invalidate: [RESOURCE_DEP] });", "toast.success('Todo deleted.');"},
		"new/+page.svelte":       {"setMessage(form, unreachable, { status: 503 });", "toast.success('Todo created.');", "await goto(`/todos/${data.id}`);", "SPA: true,", "Create todo"},
		"[id]/edit/+page.svelte": {"setMessage(form, unreachable, { status: 503 });", "toast.success('Changes saved.');", "await goto(`/todos/${saved.id}`, { invalidate: [RESOURCE_DEP] });", "SPA: true,", "Save changes"},
		"+page.ts":               {"error(503, unreachable);", "limit: perPage, offset: (page - 1) * perPage", "redirect(303, last.pathname + last.search);"},
		"form.ts":                {"error(503, unreachable);"},
	} {
		data := file(t, plan, name)
		for _, want := range wants {
			if !strings.Contains(data, want) {
				t.Errorf("%s does not contain %q", name, want)
			}
		}
	}
}

// TestEmbedPagesSameComponent checks the pages that SvelteKit keeps when only
// the id changes, for example on a client navigation from /todos/1/edit to
// /todos/2/edit. The edit form, its save, and its busy state belong to one
// item: a new id starts the form again, a new load of the same id keeps the
// draft, and a late answer for the old item changes nothing. The read view
// drops the dialog and the error of the old item.
func TestEmbedPagesSameComponent(t *testing.T) {
	t.Parallel()
	// TestEmbedPagesClients shows that both API clients get the same pages.
	plan := planSpec(t, todoSpec(t, embedAuthApp(t), "title:string", "done:bool", "due:date?"))
	edit := file(t, plan, "[id]/edit/+page.svelte")
	for _, want := range []string{
		"let original = untrack(() => data.item);",
		"async onUpdate({ form, cancel }) {",
		"const item = original;\n\t\t\tconst path = { id: item.id };\n\t\t\tsaving = item.id;",
		"if (saving === path.id) saving = null;",
		"if (original.id !== path.id) {\n",
		"\t\t\t\tcancel();\n",
		"const submitting = toStore(() => saving === data.item.id);",
		"$effect.pre(() => {\n\t\tconst item = data.item;\n\t\tif (item.id === original.id) return;\n\t\toriginal = item;\n\t\tform.reset({ data: fromItem(item), newState: fromItem(item) });\n\t});",
		"import { toStore } from 'svelte/store';",
	} {
		if !strings.Contains(edit, want) {
			t.Errorf("the edit page does not contain %q", want)
		}
	}
	// The late-answer check comes before every write to the form and before each navigation.
	check := strings.Index(edit, "if (original.id !== path.id)")
	for _, later := range []string{"setMessage(form, unreachable", "applyFailure(form", "await goto("} {
		if i := strings.Index(edit, later); i < check {
			t.Errorf("%q comes before the check of the item id", later)
		}
	}
	if strings.Contains(edit, "const { enhance, message, submitting } = form;") {
		t.Errorf("the edit page uses the busy state of superForm, which does not know the item")
	}

	item := file(t, plan, "[id]/+page.svelte")
	for _, want := range []string{
		"let deleting: number | null = $state(null);",
		"const id = item.id;\n\t\tif (deleting === id) return;",
		"if (id !== data.item.id) {",
		"let shown = untrack(() => data.item.id);",
		"confirming = false;\n\t\tfailure = '';",
		"disabled={deleting === item.id}",
	} {
		if !strings.Contains(item, want) {
			t.Errorf("the read view does not contain %q", want)
		}
	}
}

// TestEmbedPagesLive checks the scoped refresh of --live and the advice of
// spinup g live in embed mode.
func TestEmbedPagesLive(t *testing.T) {
	t.Parallel()
	root := embedAuthApp(t)
	lock := &manifest.Lock{Packs: []string{"base", "api", "web", "auth", "live"}, Answers: map[string]any{"frontend_mode": "embed"}}
	writeLock(t, root, lock.Packs)
	saved, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	saved.Answers["frontend_mode"] = "embed"
	if err := saved.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	out, err := run(t.Context(), gen.Input{Root: root, Lock: lock, Args: []string{"note", "title:string"}, Flags: liveFlags(t, "--live")})
	if err != nil {
		t.Fatal(err)
	}
	list, ok := out.Plan.Get("web/src/routes/notes/+page.svelte")
	if !ok {
		t.Fatalf("plan has no list page: %v", out.Plan.Paths())
	}
	for _, want := range []string{"import { liveRefresh } from '#lib/live.svelte.js';", "liveRefresh('notes', { invalidate: RESOURCE_DEP });"} {
		if !strings.Contains(string(list.Data), want) {
			t.Errorf("the list does not contain %q", want)
		}
	}
	form, _ := out.Plan.Get("web/src/routes/notes/form.ts")
	if !strings.Contains(string(form.Data), "export const RESOURCE_DEP = 'app:resource:notes';") {
		t.Errorf("form.ts has another dependency key:\n%s", form.Data)
	}
	for _, f := range out.Plan.Files {
		write(t, root, map[string]string{f.Path: string(f.Data)})
	}
	retrofit, err := runLive(t.Context(), gen.Input{Root: root, Lock: lock, Args: []string{"note"}, Flags: liveFlags(t, "--force")})
	if err != nil {
		t.Fatal(err)
	}
	next := strings.Join(retrofit.Next, "\n")
	if want := "import { RESOURCE_DEP } from './form';\nliveRefresh('notes', { invalidate: RESOURCE_DEP });"; !strings.Contains(next, want) {
		t.Errorf("the embed advice does not contain %q:\n%s", want, next)
	}
	// Node mode keeps its advice.
	name, err := fields.ParseName("note")
	if err != nil {
		t.Fatal(err)
	}
	if got := liveRefreshAdvice(Spec{Name: name}, false); got != "import { liveRefresh } from '#lib/live.svelte.js';\nliveRefresh('notes');" {
		t.Errorf("node advice = %q", got)
	}
}

// TestEmbedPagesClients checks that the pages are the same for both API
// clients: the heyapi wrapper keeps the typed GET, POST, PUT, and DELETE.
func TestEmbedPagesClients(t *testing.T) {
	t.Parallel()
	fetchApp := embedApp(t, []string{"auth"}, nil)
	heyApp := embedApp(t, []string{"auth"}, map[string]any{"api_client": "heyapi"})
	a, b := planSpec(t, todoSpec(t, fetchApp, goldenArgs[1:]...)), planSpec(t, todoSpec(t, heyApp, goldenArgs[1:]...))
	for i, f := range a.Files {
		if !bytes.Equal(f.Data, b.Files[i].Data) {
			t.Errorf("%s differs between the API clients", f.Path)
		}
	}
	// The same input renders the same bytes.
	again := planSpec(t, todoSpec(t, fetchApp, goldenArgs[1:]...))
	for i, f := range a.Files {
		if !bytes.Equal(f.Data, again.Files[i].Data) {
			t.Errorf("%s is not deterministic", f.Path)
		}
	}
}

// TestOtherFamilyPagePaths checks the page paths that only the other mode
// writes, and that destroy knows every path of both families.
func TestOtherFamilyPagePaths(t *testing.T) {
	t.Parallel()
	spec := todoSpec(t, embedApp(t, nil, nil), "title:string")
	node := OtherFamilyPagePaths(spec.Name, true)
	embed := OtherFamilyPagePaths(spec.Name, false)
	wantNode := []string{"web/src/routes/todos/+page.server.ts", "web/src/routes/todos/[id]/+page.server.ts", "web/src/routes/todos/[id]/edit/+page.server.ts", "web/src/routes/todos/new/+page.server.ts"}
	wantEmbed := []string{"web/src/routes/todos/+page.ts", "web/src/routes/todos/[id]/+page.ts", "web/src/routes/todos/[id]/edit/+page.ts", "web/src/routes/todos/new/+page.ts"}
	if !slices.Equal(node, wantNode) || !slices.Equal(embed, wantEmbed) {
		t.Fatalf("node-only %v, embed-only %v", node, embed)
	}
	all := Paths(spec.Name)
	for _, path := range append(slices.Clone(embedTodoPages), todoPages...) {
		if !slices.Contains(all, path) {
			t.Errorf("Paths() has no %s", path)
		}
	}
	seen := map[string]bool{}
	for _, path := range all {
		if seen[path] {
			t.Errorf("Paths() lists %s twice", path)
		}
		seen[path] = true
	}
}
