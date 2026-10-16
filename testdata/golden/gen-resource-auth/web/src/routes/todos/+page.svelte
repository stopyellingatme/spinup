<script lang="ts">
	import { enhance, type ActionResult, type SubmitFunction } from '$app/forms';
	import { afterNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
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
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Button, buttonVariants } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Checkbox } from '#lib/components/ui/checkbox/index.js';
	import * as DropdownMenu from '#lib/components/ui/dropdown-menu/index.js';
	import * as Pagination from '#lib/components/ui/pagination/index.js';
	import * as Table from '#lib/components/ui/table/index.js';
	import { priorityLabels, formatDate, itemTitle, type Item } from './schema';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	// Show the toast of a redirect once, then remove its flag from the address bar.
	afterNavigate(({ to }) => {
		if (!to?.url.searchParams.has('deleted')) return;
		toast.success('Todo deleted.');
		const url = new URL(to.url);
		url.searchParams.delete('deleted');
		void goto(url, { replace: true, shallow: true, state: page.state });
	});

	// pageHref returns the address of a page of the list.
	function pageHref(n: number): string {
		return n > 1 ? `?page=${n}` : '/todos';
	}

	// failureText returns the message of a failed action.
	function failureText(result: ActionResult, fallback: string): string {
		if (result.type === 'failure' && typeof result.data?.message === 'string') return result.data.message;
		if (result.type === 'error') return result.error.message;
		return fallback;
	}

	// The done values that a toggle has sent and the API has not saved yet, by id.
	let pending: Record<number, boolean> = $state({});
	const toggleForms: Record<number, HTMLFormElement> = {};

	// The checkbox shows the new value at once, and goes back with an error toast when the save fails.
	const toggle: SubmitFunction = ({ formData }) => {
		const id = Number(formData.get('id'));
		pending[id] = formData.get('value') === 'true';
		return async ({ result, update }) => {
			if (result.type === 'failure' || result.type === 'error') {
				toast.error(failureText(result, 'The todo was not saved.'));
			} else {
				await update({ reset: false, navigate: false });
			}
			delete pending[id];
		};
	};

	// The todo that the delete dialog asks about. It stays set while the dialog closes.
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
				toast.error(failureText(result, 'The todo was not deleted.'));
			} else {
				await update();
			}
			deleting = false;
			confirming = false;
		};
	};
</script>

<svelte:head>
	<title>Todos</title>
</svelte:head>

<PageHeader
	title="Todos"
	description={data.total === 1 ? 'One todo.' : `${data.total} todos.`}
>
	{#snippet actions()}
		<Button href="/todos/new">
			<PlusIcon aria-hidden="true" />
			New todo
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
				<h2 class="font-medium">No todos yet</h2>
				<p class="text-muted-foreground text-sm">Create the first todo, and it shows here.</p>
			</div>
			<Button href="/todos/new">
				<PlusIcon aria-hidden="true" />
				New todo
			</Button>
		</Card.Content>
	</Card.Root>
{:else}
	<Card.Root class="py-2">
		<Card.Content class="px-2">
			<Table.Root>
				<Table.Header>
					<Table.Row>
						<Table.Head class="w-10"><span class="sr-only">Done</span></Table.Head>
						<Table.Head>Title</Table.Head>
						<Table.Head>Due</Table.Head>
						<Table.Head>Priority</Table.Head>
						<Table.Head class="w-12"><span class="sr-only">Actions</span></Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each data.items as item (item.id)}
						<Table.Row>
							<Table.Cell>
								<form
									method="POST"
									action="?/toggle"
									use:enhance={toggle}
									bind:this={toggleForms[item.id]}
									class="flex items-center"
								>
									<input type="hidden" name="id" value={item.id} />
									<input type="hidden" name="value" value={String(!item.done)} />
									<Checkbox
										checked={pending[item.id] ?? item.done}
										disabled={item.id in pending}
										onCheckedChange={() => toggleForms[item.id]?.requestSubmit()}
										aria-label="Done: {itemTitle(item)}"
									/>
								</form>
							</Table.Cell>
							<Table.Cell class="max-w-xs truncate">
								<a href="/todos/{item.id}" class="font-medium underline-offset-4 hover:underline">
									{itemTitle(item)}
								</a>
							</Table.Cell>
							<Table.Cell>{item.due ? formatDate(item.due) : '—'}</Table.Cell>
							<Table.Cell><Badge variant="secondary">{priorityLabels[item.priority]}</Badge></Table.Cell>
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
												<a {...props} href="/todos/{item.id}">
													<EyeIcon aria-hidden="true" />
													View
												</a>
											{/snippet}
										</DropdownMenu.Item>
										<DropdownMenu.Item>
											{#snippet child({ props })}
												<a {...props} href="/todos/{item.id}/edit">
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
				<AlertDialog.Title>Delete this todo?</AlertDialog.Title>
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
