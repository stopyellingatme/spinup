<!-- The inputs of a todo. The new and edit pages share them. -->
<script lang="ts">
	import type { SuperForm } from 'sveltekit-superforms';
	import Field from '#lib/components/app/field.svelte';
	import { Checkbox } from '#lib/components/ui/checkbox/index.js';
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
<Field label="Done" name="done" errors={$errors.done}>
	{#snippet children({ props })}
		<Checkbox {...props} bind:checked={$values.done} />
	{/snippet}
</Field>
<Field label="Due" name="due" errors={$errors.due}>
	{#snippet children({ props })}
		<Input {...props} type="date" bind:value={$values.due} />
	{/snippet}
</Field>
