package resource

// The Svelte files of the pages in embed mode (ADR 0039 §10.5). They keep the
// labels, roles, headings, menu items, and toast texts of the node pages. The
// writes call the Go API from the browser, so they need JavaScript. Each write
// catches a transport failure, and each pending flag clears in finally, so a
// second attempt works. A page shows its toast once, right after the write.

const embedListSvelte = `<script lang="ts">
[[- if .Live ]]
	import { liveRefresh } from '#lib/live.svelte.js';
[[- end ]]
	import { invalidate } from '$app/navigation';
	import ChevronLeftIcon from '@lucide/svelte/icons/chevron-left';
	import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
	import InboxIcon from '@lucide/svelte/icons/inbox';
	import LoaderCircleIcon from '@lucide/svelte/icons/loader-circle';
	import PlusIcon from '@lucide/svelte/icons/plus';
	import { toast } from 'svelte-sonner';
	import { api } from '#lib/api/client.js';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import RowMenu, { RowMenuState } from '#lib/components/app/row-menu.svelte';
	import * as AlertDialog from '#lib/components/ui/alert-dialog/index.js';
[[- if .ListBadge ]]
	import { Badge } from '#lib/components/ui/badge/index.js';
[[- end ]]
	import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import * as Pagination from '#lib/components/ui/pagination/index.js';
	import * as Table from '#lib/components/ui/table/index.js';
[[- if .Auth ]]
	import { observedFetch } from '#lib/session-watch.js';
[[- end ]]
	import { RESOURCE_DEP, unreachable } from './form';
	import { [[ range .Enums ]][[ if .Cell ]][[ .Labels ]], [[ end ]][[ end ]][[ if .Date ]]formatDate, [[ end ]][[ if .Time ]]formatTime, [[ end ]][[ if .Toggle ]]fromItem, [[ end ]]itemTitle, problemMessage, [[ if .Toggle ]]toBody, [[ end ]]type Item } from './schema';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
[[- if .Live ]]
	// A live patch reruns only the loads of the [[ .Words ]] data, not the session load.
	liveRefresh('[[ .Feed ]]', { invalidate: RESOURCE_DEP });
[[- end ]]
[[- if .Auth ]]

	// The writes go through the session watch: a 401 asks for a new session read.
	const apiFetch = observedFetch(fetch);
[[- end ]]

	// pageHref returns the address of a page of the list.
	function pageHref(n: number): string {
		return n > 1 ? ` + "`" + `?page=${n}` + "`" + ` : '[[ .Route ]]';
	}
[[- if .Toggle ]]

	// The [[ .Toggle.Col ]] values that a toggle has sent and the API has not saved yet, by id.
	let pending: Record<number, boolean> = $state({});

	// The checkbox shows the new value at once, and goes back with an error toast when the save fails.
	// PUT replaces the whole [[ .Words ]], so the page reads the current [[ .Words ]] first. A second click while
	// the first save runs does nothing. The button stays enabled, so it keeps the focus.
	async function toggle(event: SubmitEvent) {
		event.preventDefault();
		const form = new FormData(event.currentTarget as HTMLFormElement);
		const id = Number(form.get('id'));
		if (id in pending) return;
		const value = form.get('value') === 'true';
		pending[id] = value;
		const path = { id };
		try {
			const current = await api.GET('[[ .APIPath ]]/{id}', { [[ if .Auth ]]fetch: apiFetch, [[ end ]]params: { path } });
			if (!current.data) {
				toast.error(problemMessage(current.error, current.response.status));
				return;
			}
			const saved = await api.PUT('[[ .APIPath ]]/{id}', {
				[[ if .Auth ]]fetch: apiFetch,
				[[ end ]]params: { path },
				body: { ...toBody(fromItem(current.data)[[ if .Time ]], current.data[[ end ]]), [[ .Toggle.Col ]]: value }
			});
			if (!saved.data) {
				toast.error(problemMessage(saved.error, saved.response.status));
				return;
			}
			await invalidate(RESOURCE_DEP);
		} catch (err) {
			// The request did not complete. The checkbox goes back, and the user can click again.
			console.error('[[ .Feed ]]: the toggle failed:', err);
			toast.error(unreachable);
		} finally {
			delete pending[id];
		}
	}
[[- end ]]

	// The one menu of the row buttons (see RowMenu). The page renders it once, outside the rows.
	const menu = new RowMenuState();

	// The [[ .Words ]] that the delete dialog asks about. It stays set while the dialog closes.
	let target: Item | null = $state(null);
	let confirming = $state(false);
	let deleting = $state(false);

	function confirmDelete(id: number) {
		target = data.items.find((item) => item.id === id) ?? null;
		confirming = target !== null;
	}

	async function remove(event: SubmitEvent) {
		event.preventDefault();
		if (!target || deleting) return;
		deleting = true;
		try {
			const { error: problem, response } = await api.DELETE('[[ .APIPath ]]/{id}', {
				[[ if .Auth ]]fetch: apiFetch,
				[[ end ]]params: { path: { id: target.id } }
			});
			// A [[ .Words ]] that is already gone is not an error.
			if (!response.ok && response.status !== 404) {
				toast.error(problemMessage(problem, response.status));
				return;
			}
			toast.success('[[ .Name ]] deleted.');
			// The list reads the same page again. A page past the end shows the last page.
			await invalidate(RESOURCE_DEP);
		} catch (err) {
			console.error('[[ .Feed ]]: the delete failed:', err);
			toast.error(unreachable);
		} finally {
			deleting = false;
			confirming = false;
		}
	}
</script>

<svelte:head>
	<title>[[ .Title ]]</title>
</svelte:head>

<PageHeader
	title="[[ .Title ]]"
	description={data.total === 1 ? 'One [[ .Words ]].' : ` + "`" + `${data.total} [[ .PluralWords ]].` + "`" + `}
>
	{#snippet actions()}
		<Button href="[[ .Route ]]/new">
			<PlusIcon aria-hidden="true" />
			New [[ .Words ]]
		</Button>
	{/snippet}
</PageHeader>

{#if data.total === 0}
	<Card.Root>
		<Card.Content class="flex flex-col items-center gap-4 py-10 text-center">
			<div class="bg-muted text-muted-foreground flex size-12 items-center justify-center rounded-full">
				<InboxIcon aria-hidden="true" class="size-6" />
			</div>
			<div class="grid gap-1">
				<h2 class="font-medium">No [[ .PluralWords ]] yet</h2>
				<p class="text-muted-foreground text-sm">Create the first [[ .Words ]], and it shows here.</p>
			</div>
			<Button href="[[ .Route ]]/new">
				<PlusIcon aria-hidden="true" />
				New [[ .Words ]]
			</Button>
		</Card.Content>
	</Card.Root>
{:else}
	<Card.Root class="py-2">
		<Card.Content class="px-2">
			<Table.Root>
				<Table.Header>
					<Table.Row>
[[- if .Toggle ]]
						<Table.Head class="w-10"><span class="sr-only">[[ .Toggle.Label ]]</span></Table.Head>
[[- end ]]
						<Table.Head>[[ .TitleLabel ]]</Table.Head>
[[- range .Columns ]]
						<Table.Head>[[ .Label ]]</Table.Head>
[[- end ]]
						<Table.Head class="w-12"><span class="sr-only">Actions</span></Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each data.items as item (item.id)}
						<Table.Row>
[[- if .Toggle ]]
							<Table.Cell>
								<form method="POST" onsubmit={toggle} class="flex items-center">
									<input type="hidden" name="id" value={item.id} />
									<input type="hidden" name="value" value={String(!item.[[ .Toggle.Col ]])} />
									<!-- A submit button with the checkbox role. It looks like the Checkbox component. The page sends the change to the Go API. -->
									<button
										type="submit"
										role="checkbox"
										aria-checked={pending[item.id] ?? item.[[ .Toggle.Col ]]}
										aria-label="[[ .Toggle.Label ]]: {itemTitle(item)}"
										class="border-input dark:bg-input/30 aria-checked:bg-primary aria-checked:text-primary-foreground dark:aria-checked:bg-primary aria-checked:border-primary focus-visible:border-ring focus-visible:ring-ring/50 relative flex size-4 shrink-0 items-center justify-center rounded-[4px] border transition-colors outline-none after:absolute after:-inset-x-3 after:-inset-y-2 focus-visible:ring-3"
									>
										{#if pending[item.id] ?? item.[[ .Toggle.Col ]]}
											<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="size-3.5" aria-hidden="true"><path d="M20 6 9 17l-5-5" /></svg>
										{/if}
									</button>
								</form>
							</Table.Cell>
[[- end ]]
							<Table.Cell class="max-w-xs truncate">
								<a href="[[ .Route ]]/{item.id}" class="font-medium underline-offset-4 hover:underline">
									{itemTitle(item)}
								</a>
							</Table.Cell>
[[- range .Columns ]]
							<Table.Cell>[[ .Cell ]]</Table.Cell>
[[- end ]]
							<Table.Cell class="text-right">
								<button {...menu.button(item.id, ` + "`" + `Actions for ${itemTitle(item)}` + "`" + `)}>
									<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="size-4" aria-hidden="true"><circle cx="12" cy="12" r="1" /><circle cx="19" cy="12" r="1" /><circle cx="5" cy="12" r="1" /></svg>
								</button>
							</Table.Cell>
						</Table.Row>
					{/each}
				</Table.Body>
			</Table.Root>
		</Card.Content>
	</Card.Root>

	{#if data.total > data.perPage}
		<Pagination.Root count={data.total} perPage={data.perPage} page={data.page} class="mt-6">
			{#snippet children({ pages, currentPage })}
				<Pagination.Content>
					<Pagination.Item>
						{#if currentPage > 1}
							<a href={pageHref(currentPage - 1)} class={buttonVariants({ variant: 'ghost' })}>
								<ChevronLeftIcon aria-hidden="true" />
								<span>Previous</span>
							</a>
						{/if}
					</Pagination.Item>
					{#each pages as entry (entry.key)}
						<Pagination.Item>
							{#if entry.type === 'ellipsis'}
								<Pagination.Ellipsis />
							{:else}
								<a
									href={pageHref(entry.value)}
									aria-current={entry.value === currentPage ? 'page' : undefined}
									class={buttonVariants({ variant: entry.value === currentPage ? 'outline' : 'ghost', size: 'icon' })}
								>
									{entry.value}
								</a>
							{/if}
						</Pagination.Item>
					{/each}
					<Pagination.Item>
						{#if currentPage * data.perPage < data.total}
							<a href={pageHref(currentPage + 1)} class={buttonVariants({ variant: 'ghost' })}>
								<span>Next</span>
								<ChevronRightIcon aria-hidden="true" />
							</a>
						{/if}
					</Pagination.Item>
				</Pagination.Content>
			{/snippet}
		</Pagination.Root>
	{/if}
{/if}

<!-- One menu for all rows. It opens at the button of the row that the user chose. -->
<RowMenu {menu} items={data.items} href="[[ .Route ]]" ondelete={confirmDelete} />

<AlertDialog.Root bind:open={confirming}>
	<!-- The dialog has no trigger. When it closes, the row button of the menu gets the focus back. -->
	<AlertDialog.Content
		onCloseAutoFocus={(event) => {
			event.preventDefault();
			menu.anchor?.focus();
		}}
	>
		<form method="POST" onsubmit={remove} class="grid gap-4">
			<input type="hidden" name="id" value={target?.id} />
			<AlertDialog.Header>
				<AlertDialog.Title>Delete this [[ .Words ]]?</AlertDialog.Title>
				<AlertDialog.Description>
					“{target ? itemTitle(target) : ''}” will be deleted. You cannot undo this.
				</AlertDialog.Description>
			</AlertDialog.Header>
			<AlertDialog.Footer>
				<AlertDialog.Cancel type="button" disabled={deleting}>Cancel</AlertDialog.Cancel>
				<Button type="submit" variant="destructive" disabled={deleting}>
					{#if deleting}<LoaderCircleIcon aria-hidden="true" class="animate-spin" />{/if}
					Delete
				</Button>
			</AlertDialog.Footer>
		</form>
	</AlertDialog.Content>
</AlertDialog.Root>
`

const embedItemSvelte = `<script lang="ts">
	import { onDestroy, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
	import LoaderCircleIcon from '@lucide/svelte/icons/loader-circle';
	import PencilIcon from '@lucide/svelte/icons/pencil';
	import Trash2Icon from '@lucide/svelte/icons/trash-2';
	import { toast } from 'svelte-sonner';
	import { api } from '#lib/api/client.js';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import * as AlertDialog from '#lib/components/ui/alert-dialog/index.js';
[[- if .Enums ]]
	import { Badge } from '#lib/components/ui/badge/index.js';
[[- end ]]
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
[[- if .Auth ]]
	import { observedFetch } from '#lib/session-watch.js';
[[- end ]]
	import { RESOURCE_DEP, unreachable } from '../form';
	import { [[ range .Enums ]][[ .Labels ]], [[ end ]][[ if .Date ]]formatDate, [[ end ]]formatTime, itemTitle, problemMessage } from '../schema';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	const item = $derived(data.item);
	const title = $derived(itemTitle(item));

	// The delete button asks first. The dialog sends the delete to the Go API. The dialog, the busy
	// state, and the error belong to the [[ .Words ]] that the page shows.
	let confirming = $state(false);
	// The id of the [[ .Words ]] whose delete runs.
	let deleting: number | null = $state(null);
	let failure = $state('');

	// A visit of this page ends when the id changes or the page closes. A late answer of an older
	// visit changes nothing on the page that the user sees now, and it does not move the user. The
	// id alone is not enough: the user can go from one [[ .Words ]] to another and back.
	let visit = 0;
	onDestroy(() => visit++);

	// SvelteKit keeps this page when only the id changes. The dialog and the error of the old
	// [[ .Words ]] then go away.
	let shown = untrack(() => data.item.id);
	$effect.pre(() => {
		const id = data.item.id;
		if (id === shown) return;
		shown = id;
		visit++;
		confirming = false;
		failure = '';
	});

	function confirmDelete(event: MouseEvent) {
		event.preventDefault();
		confirming = true;
	}

	async function remove(event: SubmitEvent) {
		event.preventDefault();
		const id = item.id;
		if (deleting === id) return;
		const started = visit;
		deleting = id;
		failure = '';
		try {
			const { error: problem, response } = await api.DELETE('[[ .APIPath ]]/{id}', {
				[[ if .Auth ]]fetch: observedFetch(fetch),
				[[ end ]]params: { path: { id } }
			});
			// A [[ .Words ]] that is already gone is not an error.
			const deleted = response.ok || response.status === 404;
			// The user left this visit: the answer changes nothing on the page.
			if (started !== visit) {
				if (deleted) toast.success('[[ .Name ]] deleted.');
				return;
			}
			if (!deleted) {
				failure = problemMessage(problem, response.status);
				toast.error(failure);
				return;
			}
			toast.success('[[ .Name ]] deleted.');
			await goto('[[ .Route ]]', { invalidate: [RESOURCE_DEP] });
		} catch (err) {
			console.error('[[ .Feed ]]: the delete failed:', err);
			if (started !== visit) return;
			failure = unreachable;
			toast.error(failure);
		} finally {
			if (deleting === id) deleting = null;
			if (started === visit) confirming = false;
		}
	}
</script>

<svelte:head>
	<title>{title}</title>
</svelte:head>

<div class="max-w-2xl">
	<a
		href="[[ .Route ]]"
		class="text-muted-foreground hover:text-foreground mb-4 inline-flex items-center gap-1 text-sm underline-offset-4 hover:underline"
	>
		<ArrowLeftIcon aria-hidden="true" class="size-4" />
		[[ .Title ]]
	</a>

	<PageHeader {title}>
		{#snippet actions()}
			<Button href="[[ .Route ]]/{item.id}/edit" variant="outline">
				<PencilIcon aria-hidden="true" />
				Edit
			</Button>
			<form id="delete-item" method="POST" onsubmit={remove} class="contents">
				<Button type="submit" variant="destructive" onclick={confirmDelete}>
					<Trash2Icon aria-hidden="true" />
					Delete
				</Button>
			</form>
		{/snippet}
	</PageHeader>

	{#if failure}
		<p role="alert" class="border-destructive/50 text-destructive mb-6 rounded-lg border px-3 py-2 text-sm">
			{failure}
		</p>
	{/if}

	<Card.Root>
		<Card.Content>
			<dl class="divide-y">
[[- range .Fields ]]
				<div class="grid gap-1 py-3 first:pt-0 last:pb-0 sm:grid-cols-[10rem_1fr] sm:gap-4">
					<dt class="text-muted-foreground text-sm font-medium">[[ .Label ]]</dt>
					<dd class="[[ if .ValueClass ]][[ .ValueClass ]] [[ end ]]min-w-0 text-sm">[[ .Value ]]</dd>
				</div>
[[- end ]]
				<div class="grid gap-1 py-3 first:pt-0 last:pb-0 sm:grid-cols-[10rem_1fr] sm:gap-4">
					<dt class="text-muted-foreground text-sm font-medium">Created</dt>
					<dd class="min-w-0 text-sm">{formatTime(item.created_at)}</dd>
				</div>
				<div class="grid gap-1 py-3 first:pt-0 last:pb-0 sm:grid-cols-[10rem_1fr] sm:gap-4">
					<dt class="text-muted-foreground text-sm font-medium">Updated</dt>
					<dd class="min-w-0 text-sm">{formatTime(item.updated_at)}</dd>
				</div>
			</dl>
		</Card.Content>
	</Card.Root>
</div>

<AlertDialog.Root bind:open={confirming}>
	<AlertDialog.Content>
		<AlertDialog.Header>
			<AlertDialog.Title>Delete this [[ .Words ]]?</AlertDialog.Title>
			<AlertDialog.Description>“{title}” will be deleted. You cannot undo this.</AlertDialog.Description>
		</AlertDialog.Header>
		<AlertDialog.Footer>
			<AlertDialog.Cancel disabled={deleting === item.id}>Cancel</AlertDialog.Cancel>
			<Button type="submit" form="delete-item" variant="destructive" disabled={deleting === item.id}>
				{#if deleting === item.id}<LoaderCircleIcon aria-hidden="true" class="animate-spin" />{/if}
				Delete
			</Button>
		</AlertDialog.Footer>
	</AlertDialog.Content>
</AlertDialog.Root>
`

// embedFormScript returns the imports of the new and edit pages. navigation
// lists the imports from $app/navigation, and head the imports before them.
func embedFormScript(head, navigation string) string {
	return `<script lang="ts">
` + head + `	import { ` + navigation + ` } from '$app/navigation';
	import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
	import CircleAlertIcon from '@lucide/svelte/icons/circle-alert';
	import LoaderCircleIcon from '@lucide/svelte/icons/loader-circle';
	import { toast } from 'svelte-sonner';
	import { defaults, setMessage, superForm } from 'sveltekit-superforms';
	import { zod4 } from 'sveltekit-superforms/adapters';
	import { api } from '#lib/api/client.js';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
[[- if .Auth ]]
	import { observedFetch } from '#lib/session-watch.js';
[[- end ]]
`
}

// The new page checks the values in the browser with the schema, then posts
// them to the collection and opens the new item. The edit page starts its form
// with the loaded item, then sends the full body with PUT and returns to the
// item. When only the id changes, SvelteKit keeps the page, so the edit page
// starts the form again for the new item.
// formMarkup (pages_svelte.go) gives the form card of both pages, as in node
// mode.
var (
	embedNewSvelte = embedFormScript("\timport { onDestroy } from 'svelte';\n", "goto") + `	import Fields from '../fields.svelte';
	import { applyFailure, unreachable } from '../form';
	import { schema, toBody } from '../schema';

	// SPA mode: the browser checks the values with the schema, then posts them to the Go API. The
	// field errors of the API problem show at the inputs. A failed request keeps the values.
	const adapter = zod4(schema);
	// A late answer after the user left this page does not open the new [[ .Words ]].
	let closed = false;
	onDestroy(() => (closed = true));
	const form = superForm(defaults(adapter), {
		SPA: true,
		id: '[[ .Feed ]]-new',
		validators: adapter,
		resetForm: false,
		async onUpdate({ form, cancel }) {
			if (!form.valid) return;
			const result = await api
				.POST('[[ .APIPath ]]', { [[ if .Auth ]]fetch: observedFetch(fetch), [[ end ]]body: toBody(form.data) })
				.catch((err: unknown) => {
					console.error('[[ .Feed ]]: the create failed:', err);
					return undefined;
				});
			if (closed) {
				// The user left: the answer changes nothing and does not move the user.
				cancel();
				if (result?.data) toast.success('[[ .Name ]] created.');
				return;
			}
			if (!result) {
				// The request did not complete. The values stay, and the user can submit again.
				setMessage(form, unreachable, { status: 503 });
				return;
			}
			const { data, error: problem, response } = result;
			if (!data) {
				applyFailure(form, problem, response.status);
				return;
			}
			toast.success('[[ .Name ]] created.');
			await goto(` + "`" + `[[ .Route ]]/${data.id}` + "`" + `);
		}
	});
	const { enhance, message, submitting } = form;
</script>

<svelte:head>
	<title>New [[ .Words ]]</title>
</svelte:head>

<div class="max-w-2xl">
	<a
		href="[[ .Route ]]"
		class="text-muted-foreground hover:text-foreground mb-4 inline-flex items-center gap-1 text-sm underline-offset-4 hover:underline"
	>
		<ArrowLeftIcon aria-hidden="true" class="size-4" />
		[[ .Title ]]
	</a>

	<PageHeader title="New [[ .Words ]]" description="Fill in the fields, then create the [[ .Words ]]." />
` + formMarkup("Create [[ .Words ]]", "[[ .Route ]]")

	embedEditSvelte = embedFormScript("\timport { onDestroy, untrack } from 'svelte';\n\timport { SvelteSet } from 'svelte/reactivity';\n\timport { toStore } from 'svelte/store';\n", "goto") + `	import Fields from '../../fields.svelte';
	import { RESOURCE_DEP, applyFailure, unreachable } from '../../form';
	import { fromItem, itemTitle, schema, toBody } from '../../schema';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	// SPA mode: superForm starts with the loaded [[ .Words ]]. The browser checks the values with the
	// schema, then sends the full [[ .Words ]] to the Go API with PUT. A failed request keeps the values.
	// The form, its save, and its busy state belong to one [[ .Words ]]: original.
	const adapter = zod4(schema);
	let original = untrack(() => data.item);
	// The ids of the [[ .Words ]] items whose save runs.
	const saving = new SvelteSet<number>();
	// A visit of this page ends when the id changes or the page closes. A late answer of an older
	// visit changes no value, error, or draft of the page that the user sees now, and it does not
	// move the user. The id alone is not enough: the user can go from one [[ .Words ]] to another
	// and back.
	let visit = 0;
	onDestroy(() => visit++);
	const form = superForm(defaults(fromItem(original), adapter), {
		SPA: true,
		id: '[[ .Feed ]]-edit',
		validators: adapter,
		resetForm: false,
		// superForm counts a submit as running until its onUpdate returns, also after the page shows
		// another [[ .Words ]]. With 'prevent', a held save of one [[ .Words ]] would drop the submit of the
		// next one without a message. The saving set below prevents a second save of one [[ .Words ]].
		multipleSubmits: 'allow',
		async onUpdate({ form, cancel }) {
			if (!form.valid) return;
			const item = original;
			if (saving.has(item.id)) {
				// A save of this [[ .Words ]] runs: a double submit sends no second request.
				cancel();
				return;
			}
			const path = { id: item.id };
			const started = visit;
			saving.add(item.id);
			const result = await api
				.PUT('[[ .APIPath ]]/{id}', {
					[[ if .Auth ]]fetch: observedFetch(fetch),
					[[ end ]]params: { path },
					body: toBody(form.data[[ if .Time ]], item[[ end ]])
				})
				.catch((err: unknown) => {
					console.error('[[ .Feed ]]: the save failed:', err);
					return undefined;
				})
				.finally(() => {
					saving.delete(path.id);
				});
			if (started !== visit) {
				// The user left this visit. The answer belongs to the old form, so it changes no value,
				// error, or message of the page that the user sees now.
				cancel();
				if (result?.data) toast.success('Changes saved.');
				return;
			}
			if (!result) {
				// The request did not complete. The values stay, and the user can submit again.
				setMessage(form, unreachable, { status: 503 });
				return;
			}
			const { data: saved, error: problem, response } = result;
			if (response.status === 404) {
				// The [[ .Words ]] is gone: the read view shows the 404 view.
				await goto(` + "`" + `[[ .Route ]]/${path.id}` + "`" + `, { invalidate: [RESOURCE_DEP] });
				return;
			}
			if (!saved) {
				applyFailure(form, problem, response.status);
				return;
			}
			toast.success('Changes saved.');
			await goto(` + "`" + `[[ .Route ]]/${saved.id}` + "`" + `, { invalidate: [RESOURCE_DEP] });
		}
	});
	const { enhance, message } = form;
	// The save button is busy only for the save of the [[ .Words ]] that the page shows.
	const submitting = toStore(() => saving.has(data.item.id));

	// SvelteKit keeps this page when only the id changes, for example on a client navigation from
	// [[ .Route ]]/1/edit to [[ .Route ]]/2/edit. The form then starts again with the new [[ .Words ]].
	// A new load of the same [[ .Words ]] (an invalidation) keeps the draft of the user.
	$effect.pre(() => {
		const item = data.item;
		if (item.id === original.id) return;
		original = item;
		visit++;
		form.reset({ data: fromItem(item), newState: fromItem(item) });
	});
</script>

<svelte:head>
	<title>Edit {itemTitle(data.item)}</title>
</svelte:head>

<div class="max-w-2xl">
	<a
		href="[[ .Route ]]/{data.item.id}"
		class="text-muted-foreground hover:text-foreground mb-4 inline-flex items-center gap-1 text-sm underline-offset-4 hover:underline"
	>
		<ArrowLeftIcon aria-hidden="true" class="size-4" />
		{itemTitle(data.item)}
	</a>

	<PageHeader title="Edit [[ .Words ]]" description="Change the fields, then save the changes." />
` + formMarkup("Save changes", "[[ .Route ]]/{data.item.id}")
)
