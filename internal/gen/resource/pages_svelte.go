package resource

// The Svelte files of the pages. They use the app shell components of the web
// pack (ADR 0015 §3): the page header, the field, and the toaster in the
// layout. A redirect after a create, a save, or a delete carries a flag such as
// ?created=1. The target page shows a toast once and removes the flag.

const listSvelte = `<script lang="ts">
	import { enhance, type ActionResult, type SubmitFunction } from '$app/forms';
	import { afterNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
[[- if .ListIcon ]]
	import CheckIcon from '@lucide/svelte/icons/check';
[[- end ]]
	import ChevronLeftIcon from '@lucide/svelte/icons/chevron-left';
	import ChevronRightIcon from '@lucide/svelte/icons/chevron-right';
	import EllipsisIcon from '@lucide/svelte/icons/ellipsis';
	import EyeIcon from '@lucide/svelte/icons/eye';
	import InboxIcon from '@lucide/svelte/icons/inbox';
	import LoaderCircleIcon from '@lucide/svelte/icons/loader-circle';
	import PencilIcon from '@lucide/svelte/icons/pencil';
	import PlusIcon from '@lucide/svelte/icons/plus';
	import Trash2Icon from '@lucide/svelte/icons/trash-2';
	import { toast } from 'svelte-sonner';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import * as AlertDialog from '#lib/components/ui/alert-dialog/index.js';
[[- if .ListBadge ]]
	import { Badge } from '#lib/components/ui/badge/index.js';
[[- end ]]
	import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
[[- if .Toggle ]]
	import { Checkbox } from '#lib/components/ui/checkbox/index.js';
[[- end ]]
	import * as DropdownMenu from '#lib/components/ui/dropdown-menu/index.js';
	import * as Pagination from '#lib/components/ui/pagination/index.js';
	import * as Table from '#lib/components/ui/table/index.js';
	import { [[ range .Enums ]][[ if .Cell ]][[ .Labels ]], [[ end ]][[ end ]][[ if .Date ]]formatDate, [[ end ]][[ if .Time ]]formatTime, [[ end ]]itemTitle, type Item } from './schema';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	// Show the toast of a redirect once, then remove its flag from the address bar.
	afterNavigate(({ to }) => {
		if (!to?.url.searchParams.has('deleted')) return;
		toast.success('[[ .Name ]] deleted.');
		const url = new URL(to.url);
		url.searchParams.delete('deleted');
		void goto(url, { replace: true, shallow: true, state: page.state });
	});

	// pageHref returns the address of a page of the list.
	function pageHref(n: number): string {
		return n > 1 ? ` + "`" + `?page=${n}` + "`" + ` : '[[ .Route ]]';
	}

	// failureText returns the message of a failed action.
	function failureText(result: ActionResult, fallback: string): string {
		if (result.type === 'failure' && typeof result.data?.message === 'string') return result.data.message;
		if (result.type === 'error') return result.error.message;
		return fallback;
	}
[[- if .Toggle ]]

	// The [[ .Toggle.Col ]] values that a toggle has sent and the API has not saved yet, by id.
	let pending: Record<number, boolean> = $state({});
	const toggleForms: Record<number, HTMLFormElement> = {};

	// The checkbox shows the new value at once, and goes back with an error toast when the save fails.
	const toggle: SubmitFunction = ({ formData }) => {
		const id = Number(formData.get('id'));
		pending[id] = formData.get('value') === 'true';
		return async ({ result, update }) => {
			if (result.type === 'failure' || result.type === 'error') {
				toast.error(failureText(result, 'The [[ .Words ]] was not saved.'));
			} else {
				await update({ reset: false, navigate: false });
			}
			delete pending[id];
		};
	};
[[- end ]]

	// The [[ .Words ]] that the delete dialog asks about. It stays set while the dialog closes.
	let target: Item | null = $state(null);
	let confirming = $state(false);
	let deleting = $state(false);

	function confirmDelete(item: Item) {
		target = item;
		confirming = true;
	}

	const remove: SubmitFunction = () => {
		deleting = true;
		return async ({ result, update }) => {
			if (result.type === 'failure' || result.type === 'error') {
				toast.error(failureText(result, 'The [[ .Words ]] was not deleted.'));
			} else {
				await update();
			}
			deleting = false;
			confirming = false;
		};
	};
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
								<form
									method="POST"
									action="?/toggle"
									use:enhance={toggle}
									bind:this={toggleForms[item.id]}
									class="flex items-center"
								>
									<input type="hidden" name="id" value={item.id} />
									<input type="hidden" name="value" value={String(!item.[[ .Toggle.Col ]])} />
									<Checkbox
										checked={pending[item.id] ?? item.[[ .Toggle.Col ]]}
										disabled={item.id in pending}
										onCheckedChange={() => toggleForms[item.id]?.requestSubmit()}
										aria-label="[[ .Toggle.Label ]]: {itemTitle(item)}"
									/>
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
								<DropdownMenu.Root>
									<DropdownMenu.Trigger>
										{#snippet child({ props })}
											<Button {...props} variant="ghost" size="icon-sm" aria-label="Actions for {itemTitle(item)}">
												<EllipsisIcon aria-hidden="true" />
											</Button>
										{/snippet}
									</DropdownMenu.Trigger>
									<DropdownMenu.Content align="end" class="w-40">
										<DropdownMenu.Item>
											{#snippet child({ props })}
												<a {...props} href="[[ .Route ]]/{item.id}">
													<EyeIcon aria-hidden="true" />
													View
												</a>
											{/snippet}
										</DropdownMenu.Item>
										<DropdownMenu.Item>
											{#snippet child({ props })}
												<a {...props} href="[[ .Route ]]/{item.id}/edit">
													<PencilIcon aria-hidden="true" />
													Edit
												</a>
											{/snippet}
										</DropdownMenu.Item>
										<DropdownMenu.Separator />
										<DropdownMenu.Item variant="destructive" onSelect={() => confirmDelete(item)}>
											<Trash2Icon aria-hidden="true" />
											Delete
										</DropdownMenu.Item>
									</DropdownMenu.Content>
								</DropdownMenu.Root>
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

<AlertDialog.Root bind:open={confirming}>
	<AlertDialog.Content>
		<form method="POST" action="?/delete&page={data.page}" use:enhance={remove} class="grid gap-4">
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

const itemSvelte = `<script lang="ts">
	import { enhance, type SubmitFunction } from '$app/forms';
	import { afterNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
	import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
	import LoaderCircleIcon from '@lucide/svelte/icons/loader-circle';
	import PencilIcon from '@lucide/svelte/icons/pencil';
	import Trash2Icon from '@lucide/svelte/icons/trash-2';
	import { toast } from 'svelte-sonner';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import * as AlertDialog from '#lib/components/ui/alert-dialog/index.js';
[[- if .Enums ]]
	import { Badge } from '#lib/components/ui/badge/index.js';
[[- end ]]
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { [[ range .Enums ]][[ .Labels ]], [[ end ]][[ if .Date ]]formatDate, [[ end ]]formatTime, itemTitle } from '../schema';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();

	const item = $derived(data.item);
	const title = $derived(itemTitle(item));

	// Show the toast of a redirect once, then remove its flag from the address bar.
	const flags = { created: '[[ .Name ]] created.', saved: 'Changes saved.' };
	afterNavigate(({ to }) => {
		if (!to) return;
		const url = new URL(to.url);
		for (const [flag, text] of Object.entries(flags)) {
			if (!url.searchParams.has(flag)) continue;
			toast.success(text);
			url.searchParams.delete(flag);
		}
		if (url.search !== to.url.search) void goto(url, { replace: true, shallow: true, state: page.state });
	});

	// Without JavaScript, the delete button posts the form at once. With JavaScript, it asks first.
	let confirming = $state(false);
	let deleting = $state(false);

	function confirmDelete(event: MouseEvent) {
		event.preventDefault();
		confirming = true;
	}

	const remove: SubmitFunction = () => {
		deleting = true;
		return async ({ result, update }) => {
			if (result.type === 'failure') {
				toast.error(String(result.data?.message ?? 'The [[ .Words ]] was not deleted.'));
			} else {
				await update();
			}
			deleting = false;
			confirming = false;
		};
	};
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
			<form id="delete-item" method="POST" action="?/delete" use:enhance={remove} class="contents">
				<Button type="submit" variant="destructive" onclick={confirmDelete}>
					<Trash2Icon aria-hidden="true" />
					Delete
				</Button>
			</form>
		{/snippet}
	</PageHeader>

	{#if form?.message}
		<p role="alert" class="border-destructive/50 text-destructive mb-6 rounded-lg border px-3 py-2 text-sm">
			{form.message}
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
			<AlertDialog.Cancel disabled={deleting}>Cancel</AlertDialog.Cancel>
			<Button type="submit" form="delete-item" variant="destructive" disabled={deleting}>
				{#if deleting}<LoaderCircleIcon aria-hidden="true" class="animate-spin" />{/if}
				Delete
			</Button>
		</AlertDialog.Footer>
	</AlertDialog.Content>
</AlertDialog.Root>
`

// formScript, formBody, and formMarkup are the parts of the new and edit
// pages that they share.
const formScript = `<script lang="ts">
	import { untrack } from 'svelte';
	import ArrowLeftIcon from '@lucide/svelte/icons/arrow-left';
	import CircleAlertIcon from '@lucide/svelte/icons/circle-alert';
	import LoaderCircleIcon from '@lucide/svelte/icons/loader-circle';
	import { superForm } from 'sveltekit-superforms';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
`

const formBody = `
	let { data }: PageProps = $props();

	// superForm reads the first form data once. Later page data reaches it through the page store.
	// The server checks the values with the schema, and the errors show at the inputs.
	const form = superForm(untrack(() => data.form));
	const { enhance, message, submitting } = form;
</script>
`

// formMarkup returns the form card of a page, with the label of its submit
// button and the address of its cancel button.
func formMarkup(submit, cancel string) string {
	return `
	<Card.Root>
		<Card.Content>
			<form method="POST" use:enhance novalidate class="grid gap-6">
				{#if $message}
					<div role="alert" class="border-destructive/50 text-destructive flex items-start gap-2 rounded-lg border px-3 py-2 text-sm">
						<CircleAlertIcon aria-hidden="true" class="mt-0.5 size-4 shrink-0" />
						<p>{$message}</p>
					</div>
				{/if}
				<Fields {form} />
				<div class="flex flex-wrap gap-2">
					<Button type="submit" disabled={$submitting}>
						{#if $submitting}<LoaderCircleIcon aria-hidden="true" class="animate-spin" />{/if}
						` + submit + `
					</Button>
					<Button href="` + cancel + `" variant="outline">Cancel</Button>
				</div>
			</form>
		</Card.Content>
	</Card.Root>
</div>
`
}

var newSvelte = formScript + `	import Fields from '../fields.svelte';
	import type { PageProps } from './$types';
` + formBody + `
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

var editSvelte = formScript + `	import Fields from '../../fields.svelte';
	import { itemTitle } from '../../schema';
	import type { PageProps } from './$types';
` + formBody + `
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

const fieldsSvelte = `<!-- The inputs of a [[ .Words ]]. The new and edit pages share them. -->
<script lang="ts">
	import type { SuperForm } from 'sveltekit-superforms';
	import Field from '#lib/components/app/field.svelte';
[[- if .Checkbox ]]
	import { Checkbox } from '#lib/components/ui/checkbox/index.js';
[[- end ]]
[[- if .Input ]]
	import { Input } from '#lib/components/ui/input/index.js';
[[- end ]]
	import { [[ range .Enums ]][[ .Labels ]], [[ end ]]type Values } from './schema';

	let { form }: { form: SuperForm<Values> } = $props();

	// The stores of the form. A page creates its form once.
	const values = $derived(form.form);
	const errors = $derived(form.errors);
[[- if .Control ]]

	// The look of the Input component, for the native textarea and select elements.
	const control =
		'border-input dark:bg-input/30 focus-visible:border-ring focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 w-full min-w-0 rounded-lg border bg-transparent px-2.5 py-1 text-base outline-none transition-colors focus-visible:ring-3 aria-invalid:ring-3 md:text-sm';
[[- end ]]
</script>
[[ range .Fields ]]
<Field label="[[ .Label ]]" name="[[ .Col ]]" errors={$errors.[[ .Col ]]}[[ if .Description ]] description="[[ .Description ]]"[[ end ]]>
	{#snippet children({ props })}
		[[ indent .Control "\t\t" ]]
	{/snippet}
</Field>
[[- end ]]
`
