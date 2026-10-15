package resource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// The pages part writes the SvelteKit pages of a resource when the app has
// the web pack (ADR 0008 §4). The pages call the Go API through the typed
// client, so the part requests the client post-step.
func init() {
	RegisterPart(Part{Name: "pages", Plan: planPages, Paths: pagePaths})
}

// pagesDir returns the route directory of a resource, such as
// web/src/routes/line-items.
func pagesDir(name fields.Name) string { return "web/src/routes/" + name.PluralKebab() }

// pageFiles maps the files of the pages part, relative to pagesDir, to their
// templates.
var pageFiles = []struct{ path, source string }{
	{"+page.server.ts", listServerTS},
	{"+page.svelte", listSvelte},
	{"[id]/+page.server.ts", itemServerTS},
	{"[id]/+page.svelte", itemSvelte},
	{"fields.svelte", fieldsSvelte},
	{"form.ts", formTS},
	{"new/+page.server.ts", newServerTS},
	{"new/+page.svelte", newSvelte},
}

func pagePaths(name fields.Name) []string {
	paths := make([]string, 0, len(pageFiles))
	for _, f := range pageFiles {
		paths = append(paths, pagesDir(name)+"/"+f.path)
	}
	return paths
}

// hasWeb reports whether the lock at root lists the web pack. A project
// without a lock has no web pack.
func hasWeb(root string) (bool, error) {
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return slices.Contains(lock.Packs, "web"), nil
}

func planPages(_ context.Context, spec Spec) (vfs.Plan, []gen.PostStep, error) {
	web, err := hasWeb(spec.Root)
	if err != nil || !web {
		return vfs.Plan{}, nil, err
	}
	data := newPageData(spec)
	var plan vfs.Plan
	for _, f := range pageFiles {
		out, err := renderPage(f.path, f.source, data)
		if err != nil {
			return vfs.Plan{}, nil, err
		}
		if err := plan.Add(vfs.File{Path: pagesDir(spec.Name) + "/" + f.path, Data: out, Class: string(manifest.Scaffold)}); err != nil {
			return vfs.Plan{}, nil, err
		}
	}
	return plan, []gen.PostStep{gen.PostClient}, nil
}

func renderPage(name, source string, data pageData) ([]byte, error) {
	t, err := template.New(name).Delims("[[", "]]").Option("missingkey=error").Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse the page template %s: %w\nTry: report this generator bug", name, err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return nil, fmt.Errorf("render the page template %s: %w\nTry: report this generator bug", name, err)
	}
	return b.Bytes(), nil
}

// pageData is the input of the page templates.
type pageData struct {
	// Pascal is the schema name, such as LineItem.
	Pascal string
	// Words and PluralWords name the resource in text, such as "line item".
	Words, PluralWords string
	// Name and Title are Words and PluralWords with an upper case first letter.
	Name, Title string
	// Route is the web path of the list page, such as /line-items.
	Route string
	// APIPath is the API path of the list, such as /api/line_items.
	APIPath string
	// Fields are the form fields, in argument order.
	Fields []pageField
	// The form.ts helpers that the fields use.
	Optional, Integer, Number, Time, Object bool
	// Control is true when a field uses a native textarea or select.
	Control bool
}

// pageField is one field of the pages.
type pageField struct {
	// Col is the JSON property and the form input name.
	Col string
	// ValueType is the TypeScript type of the form value: string or boolean.
	ValueType string
	// Empty is the form value of a new item.
	Empty string
	// ToValue converts item.<Col> to the form value.
	ToValue string
	// ToBody converts values.<Col> to the request property.
	ToBody string
	// Read reads the form value from form.
	Read string
	// Cell is the list cell markup.
	Cell string
	// Input is the markup of the label, the input, and the error message.
	Input string
}

func words(snake string) string { return strings.ReplaceAll(snake, "_", " ") }

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func newPageData(spec Spec) pageData {
	d := pageData{
		Pascal:      spec.Name.Pascal(),
		Words:       words(spec.Name.Snake()),
		PluralWords: words(spec.Name.PluralSnake()),
		Route:       "/" + spec.Name.PluralKebab(),
		APIPath:     "/api/" + spec.Name.PluralSnake(),
	}
	d.Name, d.Title = capitalize(d.Words), capitalize(d.PluralWords)
	for _, f := range spec.Fields {
		d.Fields = append(d.Fields, d.field(f))
	}
	return d
}

// field maps one field to its form value, request property, list cell, and
// input. The inputs follow the field table: a text input, a textarea for text
// and json, a checkbox for bool, a number input for numbers and refs, date
// and datetime-local inputs, and a select for enums.
func (d *pageData) field(f fields.Field) pageField {
	col := f.Column()
	label := capitalize(words(f.Name.Snake()))
	if f.Type.Ref != "" {
		label += " ID"
	}
	p := pageField{
		Col:       col,
		ValueType: "string",
		Empty:     "''",
		Read:      fmt.Sprintf("text(form, '%s')", col),
		ToValue:   "item." + col,
		ToBody:    "values." + col,
		Cell:      fmt.Sprintf("{item.%s}", col),
	}
	if f.Nullable {
		p.ToValue = fmt.Sprintf("item.%s ?? ''", col)
		p.Cell = fmt.Sprintf("{item.%s ?? ''}", col)
	}
	// nullable wraps a conversion so that an empty input sends null.
	nullable := func(conv string) string {
		if !f.Nullable {
			return conv
		}
		return fmt.Sprintf("values.%s.trim() === '' ? null : %s", col, conv)
	}
	required := ""
	if !f.Nullable {
		required = " required"
	}
	attrs := fmt.Sprintf(`id="%s" name="%s"`, col, col)
	invalid := fmt.Sprintf(`aria-invalid={errors.%s ? true : undefined}`, col)
	input := func(typ, extra string) string {
		return fmt.Sprintf(`<Input %s type="%s" value={values.%s}%s%s %s />`, attrs, typ, col, extra, required, invalid)
	}
	var control string
	switch f.Type.Name {
	case "bool":
		if f.Nullable {
			d.Control = true
			p.ToValue = fmt.Sprintf("item.%s === null ? '' : String(item.%s)", col, col)
			p.ToBody = fmt.Sprintf("values.%s === '' ? null : values.%s === 'true'", col, col)
			p.Cell = fmt.Sprintf("{item.%s === null ? '' : item.%s ? 'Yes' : 'No'}", col, col)
			control = fmt.Sprintf(`<select %s class={control} %s>
	<option value="" selected={values.%s === ''}>Unset</option>
	<option value="true" selected={values.%s === 'true'}>True</option>
	<option value="false" selected={values.%s === 'false'}>False</option>
</select>`, attrs, invalid, col, col, col)
			break
		}
		p.ValueType, p.Empty = "boolean", "false"
		p.Read = fmt.Sprintf("form.get('%s') === 'on'", col)
		p.ToValue = "item." + col
		p.Cell = fmt.Sprintf("{item.%s ? 'Yes' : 'No'}", col)
		p.Input = fmt.Sprintf(`<div class="grid gap-2">
	<div class="flex items-center gap-2">
		<input %s type="checkbox" checked={values.%s} class="accent-primary size-4" />
		<Label for="%s">%s</Label>
	</div>
	%s
</div>`, attrs, col, col, label, errorLine(col))
		return p
	case "int", "bigint", "ref":
		d.Integer = true
		p.ToValue = fmt.Sprintf("String(item.%s)", col)
		if f.Nullable {
			p.ToValue = fmt.Sprintf("item.%s === null ? '' : String(item.%s)", col, col)
		}
		p.ToBody = nullable(fmt.Sprintf("integer(values.%s, '%s', errors)", col, col))
		control = input("number", ` step="1"`)
	case "float":
		d.Number = true
		p.ToValue = fmt.Sprintf("String(item.%s)", col)
		if f.Nullable {
			p.ToValue = fmt.Sprintf("item.%s === null ? '' : String(item.%s)", col, col)
		}
		p.ToBody = nullable(fmt.Sprintf("number(values.%s, '%s', errors)", col, col))
		control = input("number", ` step="any"`)
	case "date":
		control = input("date", "")
	case "time":
		d.Time = true
		p.ToValue = fmt.Sprintf("localTime(item.%s)", col)
		if f.Nullable {
			p.ToValue = fmt.Sprintf("item.%s === null ? '' : localTime(item.%s)", col, col)
		}
		p.ToBody = nullable(fmt.Sprintf("time(values.%s, '%s', errors, original?.%s)", col, col, col))
		control = input("datetime-local", ` step="1"`) + "\n" + `<p class="text-muted-foreground text-xs">UTC</p>`
	case "json":
		d.Object, d.Control = true, true
		p.ToValue = fmt.Sprintf("JSON.stringify(item.%s, null, 2)", col)
		p.Cell = fmt.Sprintf("{JSON.stringify(item.%s)}", col)
		if f.Nullable {
			p.ToValue = fmt.Sprintf("item.%s === null ? '' : JSON.stringify(item.%s, null, 2)", col, col)
			p.Cell = fmt.Sprintf("{item.%s === null ? '' : JSON.stringify(item.%s)}", col, col)
		}
		p.ToBody = nullable(fmt.Sprintf("object(values.%s, '%s', errors)", col, col))
		if !f.Nullable {
			p.Empty = "'{}'"
		}
		control = fmt.Sprintf(`<textarea %s rows="4" value={values.%s} class={[control, 'font-mono']}%s %s></textarea>`, attrs, col, required, invalid)
	case "text":
		d.Control = true
		control = fmt.Sprintf(`<textarea %s rows="4" value={values.%s} class={control}%s %s></textarea>`, attrs, col, required, invalid)
	case "enum":
		d.Control = true
		if !f.Nullable {
			p.Empty = "'" + f.Type.Enum[0] + "'"
		}
		var b strings.Builder
		fmt.Fprintf(&b, `<select %s class={control}%s %s>`, attrs, required, invalid)
		if f.Nullable {
			fmt.Fprintf(&b, "\n\t"+`<option value="" selected={values.%s === ''}>None</option>`, col)
		}
		for _, v := range f.Type.Enum {
			fmt.Fprintf(&b, "\n\t"+`<option value="%s" selected={values.%s === '%s'}>%s</option>`, v, col, v, words(v))
		}
		b.WriteString("\n</select>")
		control = b.String()
		p.ToBody = fmt.Sprintf("values.%s as Input['%s']", col, col)
		if f.Nullable {
			p.ToBody = fmt.Sprintf("(values.%s === '' ? null : values.%s) as Input['%s']", col, col, col)
		}
	case "decimal":
		control = input("text", ` inputmode="decimal"`)
	default: // string, uuid
		control = input("text", "")
	}
	if p.ToBody == "values."+col && f.Nullable {
		d.Optional = true
		p.ToBody = fmt.Sprintf("optional(values.%s)", col)
	}
	p.Input = fmt.Sprintf(`<div class="grid gap-2">
	<Label for="%s">%s</Label>
	%s
	%s
</div>`, col, label, indent(control, "\t"), indent(errorLine(col), "\t"))
	return p
}

func errorLine(col string) string {
	return fmt.Sprintf(`{#if errors.%s}<p class="text-destructive text-sm">{errors.%s}</p>{/if}`, col, col)
}

// indent adds prefix to every line after the first.
func indent(s, prefix string) string { return strings.ReplaceAll(s, "\n", "\n"+prefix) }

// The page templates. They use [[ ]] delimiters, so the Svelte braces need no
// escapes.
const formTS = `// The form of a [[ .Words ]]: the values that the inputs show, and their conversion to the
// JSON body of the API. Generated by spinup g resource. The application owns this file.
import type { components } from '#lib/api/schema.js';

/** Item is a [[ .Words ]] as the API returns it. */
export type Item = components['schemas']['[[ .Pascal ]]'];
/** Input is the JSON body of a create or an update. */
export type Input = components['schemas']['[[ .Pascal ]]Input'];
/** Problem is an API error (RFC 9457 problem details). */
export type Problem = components['schemas']['Problem'];

/** Values holds the form values as the inputs show them. */
export interface Values {
[[- range .Fields ]]
	[[ .Col ]]: [[ .ValueType ]];
[[- end ]]
}

/** Errors maps an input name to its error message. */
export type Errors = Record<string, string>;

/** empty holds the values of a new [[ .Words ]]. */
export const empty: Values = {
[[- range $i, $f := .Fields ]][[ if $i ]],[[ end ]]
	[[ $f.Col ]]: [[ $f.Empty ]]
[[- end ]]
};

/** toValues returns the form values of a [[ .Words ]]. */
export function toValues(item: Item): Values {
	return {
[[- range $i, $f := .Fields ]][[ if $i ]],[[ end ]]
		[[ $f.Col ]]: [[ $f.ToValue ]]
[[- end ]]
	};
}

/**
 * parse reads a submitted form. It returns the values to show again, the request body, and
 * the errors of values that cannot be converted. Send the body only when there are no errors.
 */
export function parse(form: FormData[[ if .Time ]], original?: Item[[ end ]]): { values: Values; body: Input; errors: Errors } {
	const errors: Errors = {};
	const values: Values = {
[[- range $i, $f := .Fields ]][[ if $i ]],[[ end ]]
		[[ $f.Col ]]: [[ $f.Read ]]
[[- end ]]
	};
	const body: Input = {
[[- range $i, $f := .Fields ]][[ if $i ]],[[ end ]]
		[[ $f.Col ]]: [[ $f.ToBody ]]
[[- end ]]
	};
	return { values, body, errors };
}

/** problemErrors returns the field errors of an API problem, by input name. */
export function problemErrors(problem: Problem | undefined): Errors {
	const errors: Errors = {};
	for (const e of problem?.errors ?? []) errors[e.field] = e.message;
	return errors;
}

/** message returns the text of an API problem. */
export function message(problem: Problem | undefined, status: number): string {
	return problem?.detail ?? problem?.title ?? 'The API returned status ' + status + '.';
}

function text(form: FormData, name: string): string {
	const value = form.get(name);
	return typeof value === 'string' ? value : '';
}
[[- if .Optional ]]

function optional(value: string): string | null {
	return value.trim() === '' ? null : value;
}
[[- end ]]
[[- if .Integer ]]

function integer(value: string, name: string, errors: Errors): number {
	const n = Number(value);
	if (value.trim() === '' || !Number.isSafeInteger(n)) errors[name] = 'must be a whole number';
	return n;
}
[[- end ]]
[[- if .Number ]]

function number(value: string, name: string, errors: Errors): number {
	const n = Number(value);
	if (value.trim() === '' || !Number.isFinite(n)) errors[name] = 'must be a number';
	return n;
}
[[- end ]]
[[- if .Time ]]

// The datetime-local inputs show UTC times.
function time(value: string, name: string, errors: Errors, original?: string | null): string {
	const t = new Date(value + 'Z');
	if (Number.isNaN(t.getTime())) {
		errors[name] = 'must be a date and time';
		return value;
	}
	// Browsers can omit zero seconds. Compare instants at the displayed precision.
	if (original && t.getTime() === new Date(localTime(original) + 'Z').getTime()) return original;
	return t.toISOString();
}

function localTime(value: string): string {
	return new Date(value).toISOString().slice(0, 19);
}
[[- end ]]
[[- if .Object ]]

function object(value: string, name: string, errors: Errors): Record<string, unknown> {
	try {
		const parsed: unknown = JSON.parse(value);
		if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
			return parsed as Record<string, unknown>;
		}
	} catch {
		// Reported below.
	}
	errors[name] = 'must be a JSON object';
	return {};
}
[[- end ]]
`

const fieldsSvelte = `<!-- The inputs of a [[ .Words ]]. The new and edit pages share them. -->
<script lang="ts">
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import type { Errors, Values } from './form';

	let { values, errors = {} }: { values: Values; errors?: Errors } = $props();
[[- if .Control ]]

	// The look of the Input component, for the native textarea and select elements.
	const control =
		'border-input dark:bg-input/30 focus-visible:border-ring focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 w-full min-w-0 rounded-lg border bg-transparent px-2.5 py-1 text-base outline-none focus-visible:ring-3 aria-invalid:ring-3 md:text-sm';
[[- end ]]
</script>
[[ range .Fields ]]
[[ .Input ]]
[[ end -]]
`

const listServerTS = `import { error } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
import { message } from './form';
import type { PageServerLoad } from './$types';

// Server-side API calls go to PUBLIC_ORIGIN. handleFetch sends them to the Go API with the cookie.
export const load: PageServerLoad = async ({ fetch }) => {
	const { data, error: problem, response } = await api.GET('[[ .APIPath ]]', {
		fetch,
		baseUrl: __PUBLIC_ORIGIN__,
		params: { query: { limit: 50 } }
	});
	if (!data) error(response.status, message(problem, response.status));
	return { items: data.items, total: data.total };
};
`

const listSvelte = `<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
</script>

<svelte:head>
	<title>[[ .Title ]]</title>
</svelte:head>

<Card.Root>
	<Card.Header>
		<Card.Title>[[ .Title ]]</Card.Title>
		<Card.Description>{data.total} in total.</Card.Description>
		<Card.Action>
			<Button href="[[ .Route ]]/new">New [[ .Words ]]</Button>
		</Card.Action>
	</Card.Header>
	<Card.Content class="overflow-x-auto">
		{#if data.items.length === 0}
			<p class="text-muted-foreground text-sm">No [[ .PluralWords ]] yet.</p>
		{:else}
			<table class="w-full text-left text-sm">
				<thead class="text-muted-foreground border-b">
					<tr>
						<th class="py-2 pr-4 font-medium">ID</th>
[[- range .Fields ]]
						<th class="py-2 pr-4 font-medium">[[ .Col ]]</th>
[[- end ]]
					</tr>
				</thead>
				<tbody>
					{#each data.items as item (item.id)}
						<tr class="border-b last:border-0">
							<td class="py-2 pr-4"><a class="font-medium hover:underline" href="[[ .Route ]]/{item.id}">{item.id}</a></td>
[[- range .Fields ]]
							<td class="max-w-xs truncate py-2 pr-4">[[ .Cell ]]</td>
[[- end ]]
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	</Card.Content>
</Card.Root>
`

const newServerTS = `import { fail, redirect } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
import { empty, message, parse, problemErrors } from '../form';
import type { Actions, PageServerLoad } from './$types';

export const load: PageServerLoad = () => ({ values: empty });

export const actions: Actions = {
	default: async ({ request, fetch }) => {
		const { values, body, errors } = parse(await request.formData());
		if (Object.keys(errors).length > 0) {
			return fail(422, { values, errors, message: 'Correct the marked fields.' });
		}
		const { data, error: problem, response } = await api.POST('[[ .APIPath ]]', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			body
		});
		if (!data) {
			return fail(response.status, {
				values,
				errors: problemErrors(problem),
				message: message(problem, response.status)
			});
		}
		redirect(303, '[[ .Route ]]/' + data.id);
	}
};
`

const newSvelte = `<script lang="ts">
	import { enhance } from '$app/forms';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import Fields from '../fields.svelte';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();
</script>

<svelte:head>
	<title>New [[ .Words ]]</title>
</svelte:head>

<Card.Root class="mx-auto max-w-xl">
	<Card.Header>
		<Card.Title>New [[ .Words ]]</Card.Title>
	</Card.Header>
	<Card.Content>
		<form method="POST" use:enhance class="grid gap-4">
			{#if form?.message}
				<p class="text-destructive text-sm" role="alert">{form.message}</p>
			{/if}
			<Fields values={form?.values ?? data.values} errors={form?.errors} />
			<div class="flex gap-2">
				<Button type="submit">Create</Button>
				<Button href="[[ .Route ]]" variant="outline">Cancel</Button>
			</div>
		</form>
	</Card.Content>
</Card.Root>
`

const itemServerTS = `import { error, fail, redirect } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
import { message, parse, problemErrors, toValues } from '../form';
import type { Actions, PageServerLoad } from './$types';

// id returns the route id, or 0 when it is not a positive whole number.
function id(param: string): number {
	const n = Number(param);
	return Number.isSafeInteger(n) && n > 0 ? n : 0;
}

export const load: PageServerLoad = async ({ fetch, params }) => {
	const path = { id: id(params.id) };
	if (path.id === 0) error(404, 'This [[ .Words ]] does not exist.');
	const { data, error: problem, response } = await api.GET('[[ .APIPath ]]/{id}', {
		fetch,
		baseUrl: __PUBLIC_ORIGIN__,
		params: { path }
	});
	if (response.status === 404) error(404, 'This [[ .Words ]] does not exist.');
	if (!data) error(response.status, message(problem, response.status));
	return { item: data, values: toValues(data) };
};

export const actions: Actions = {
	update: async ({ request, fetch, params }) => {
[[- if .Time ]]
		const { data: original, error: originalProblem, response: originalResponse } = await api.GET('[[ .APIPath ]]/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path: { id: id(params.id) } }
		});
		if (!original) error(originalResponse.status, message(originalProblem, originalResponse.status));
[[- end ]]
		const { values, body, errors } = parse(await request.formData()[[ if .Time ]], original[[ end ]]);
		if (Object.keys(errors).length > 0) {
			return fail(422, { values, errors, message: 'Correct the marked fields.' });
		}
		const { data, error: problem, response } = await api.PUT('[[ .APIPath ]]/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path: { id: id(params.id) } },
			body
		});
		if (!data) {
			return fail(response.status, {
				values,
				errors: problemErrors(problem),
				message: message(problem, response.status)
			});
		}
		redirect(303, '[[ .Route ]]/' + data.id);
	},
	delete: async ({ fetch, params }) => {
		const { error: problem, response } = await api.DELETE('[[ .APIPath ]]/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path: { id: id(params.id) } }
		});
		if (!response.ok) {
			return fail(response.status, { message: message(problem, response.status) });
		}
		redirect(303, '[[ .Route ]]');
	}
};
`

const itemSvelte = `<script lang="ts">
	import { enhance } from '$app/forms';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import Fields from '../fields.svelte';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();
</script>

<svelte:head>
	<title>[[ .Name ]] {data.item.id}</title>
</svelte:head>

<Card.Root class="mx-auto max-w-xl">
	<Card.Header>
		<Card.Title>[[ .Name ]] {data.item.id}</Card.Title>
		<Card.Description>
			Created {data.item.created_at}. Updated {data.item.updated_at}.
		</Card.Description>
	</Card.Header>
	<Card.Content>
		<form method="POST" action="?/update" use:enhance class="grid gap-4">
			{#if form?.message}
				<p class="text-destructive text-sm" role="alert">{form.message}</p>
			{/if}
			<Fields values={form?.values ?? data.values} errors={form?.errors} />
			<div class="flex gap-2">
				<Button type="submit">Save</Button>
				<Button href="[[ .Route ]]" variant="outline">Back</Button>
			</div>
		</form>
	</Card.Content>
	<Card.Footer>
		<form
			method="POST"
			action="?/delete"
			use:enhance={({ cancel }) => {
				if (!confirm('Delete this [[ .Words ]]?')) cancel();
			}}
		>
			<Button type="submit" variant="destructive">Delete</Button>
		</form>
	</Card.Footer>
</Card.Root>
`
