<!-- The inputs of a todo. The new and edit pages share them. -->
<script lang="ts">
	import type { SuperForm } from 'sveltekit-superforms';
	import Field from '#lib/components/app/field.svelte';
	import { Checkbox } from '#lib/components/ui/checkbox/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { priorityLabels, type Values } from './schema';

	let { form }: { form: SuperForm<Values> } = $props();

	// The stores of the form. A page creates its form once.
	const values = $derived(form.form);
	const errors = $derived(form.errors);

	// The look of the Input component, for the native textarea and select elements.
	const control =
		'border-input dark:bg-input/30 focus-visible:border-ring focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 w-full min-w-0 rounded-lg border bg-transparent px-2.5 py-1 text-base outline-none transition-colors focus-visible:ring-3 aria-invalid:ring-3 md:text-sm';
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
<Field label="Priority" name="priority" errors={$errors.priority}>
	{#snippet children({ props })}
		<select {...props} bind:value={$values.priority} class={[control, 'h-8']} required>
			{#each Object.entries(priorityLabels) as [value, label] (value)}
				<option {value}>{label}</option>
			{/each}
		</select>
	{/snippet}
</Field>
