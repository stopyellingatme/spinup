<script lang="ts">
	import { enhance } from '$app/forms';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import Fields from '../fields.svelte';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();
</script>

<svelte:head>
	<title>Todo {data.item.id}</title>
</svelte:head>

<Card.Root class="mx-auto max-w-xl">
	<Card.Header>
		<Card.Title>Todo {data.item.id}</Card.Title>
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
				<Button href="/todos" variant="outline">Back</Button>
			</div>
		</form>
	</Card.Content>
	<Card.Footer>
		<form
			method="POST"
			action="?/delete"
			use:enhance={({ cancel }) => {
				if (!confirm('Delete this todo?')) cancel();
			}}
		>
			<Button type="submit" variant="destructive">Delete</Button>
		</form>
	</Card.Footer>
</Card.Root>
