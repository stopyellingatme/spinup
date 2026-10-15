<script lang="ts">
	import { enhance } from '$app/forms';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import Fields from '../fields.svelte';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();
</script>

<svelte:head>
	<title>New todo</title>
</svelte:head>

<Card.Root class="mx-auto max-w-xl">
	<Card.Header>
		<Card.Title>New todo</Card.Title>
	</Card.Header>
	<Card.Content>
		<form method="POST" use:enhance class="grid gap-4">
			{#if form?.message}
				<p class="text-destructive text-sm" role="alert">{form.message}</p>
			{/if}
			<Fields values={form?.values ?? data.values} errors={form?.errors} />
			<div class="flex gap-2">
				<Button type="submit">Create</Button>
				<Button href="/todos" variant="outline">Cancel</Button>
			</div>
		</form>
	</Card.Content>
</Card.Root>
