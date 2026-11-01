<!-- The inputs of a note. The new and edit pages share them. -->
<script lang="ts">
	import type { SuperForm } from 'sveltekit-superforms';
	import Field from '#lib/components/app/field.svelte';
	import { Input } from '#lib/components/ui/input/index.js';
	import { type Values } from './schema';

	let { form }: { form: SuperForm<Values> } = $props();

	// The stores of the form. A page creates its form once.
	const values = $derived(form.form);
	const errors = $derived(form.errors);
</script>

<Field label="Title" name="title" errors={$errors.title}>
	{#snippet children({ props })}
		<Input {...props} bind:value={$values.title} required />
	{/snippet}
</Field>
<Field label="Api token" name="api_token" errors={$errors.api_token}>
	{#snippet children({ props })}
		<Input {...props} bind:value={$values.api_token} required />
	{/snippet}
</Field>
