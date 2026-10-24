<script lang="ts">
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
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { formatDate, formatTime, itemTitle } from '../schema';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();

	const item = $derived(data.item);
	const title = $derived(itemTitle(item));

	// Show the toast of a redirect once, then remove its flag from the address bar.
	const flags = { created: 'Todo created.', saved: 'Changes saved.' };
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
				toast.error(String(result.data?.message ?? 'The todo was not deleted.'));
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
		href="/todos"
		class="text-muted-foreground hover:text-foreground mb-4 inline-flex items-center gap-1 text-sm underline-offset-4 hover:underline"
	>
		<ArrowLeftIcon aria-hidden="true" class="size-4" />
		Todos
	</a>

	<PageHeader {title}>
		{#snippet actions()}
			<Button href="/todos/{item.id}/edit" variant="outline">
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
				<div class="grid gap-1 py-3 first:pt-0 last:pb-0 sm:grid-cols-[10rem_1fr] sm:gap-4">
					<dt class="text-muted-foreground text-sm font-medium">Title</dt>
					<dd class="min-w-0 text-sm">{item.title}</dd>
				</div>
				<div class="grid gap-1 py-3 first:pt-0 last:pb-0 sm:grid-cols-[10rem_1fr] sm:gap-4">
					<dt class="text-muted-foreground text-sm font-medium">Done</dt>
					<dd class="min-w-0 text-sm">{item.done ? 'Yes' : 'No'}</dd>
				</div>
				<div class="grid gap-1 py-3 first:pt-0 last:pb-0 sm:grid-cols-[10rem_1fr] sm:gap-4">
					<dt class="text-muted-foreground text-sm font-medium">Due</dt>
					<dd class="min-w-0 text-sm">{item.due ? formatDate(item.due) : '—'}</dd>
				</div>
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
			<AlertDialog.Title>Delete this todo?</AlertDialog.Title>
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
