<!-- The inputs of a todo. The new and edit pages share them. -->
<script lang="ts">
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import type { Errors, Values } from './form';

	let { values, errors = {} }: { values: Values; errors?: Errors } = $props();

	// The look of the Input component, for the native textarea and select elements.
	const control =
		'border-input dark:bg-input/30 focus-visible:border-ring focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 w-full min-w-0 rounded-lg border bg-transparent px-2.5 py-1 text-base outline-none focus-visible:ring-3 aria-invalid:ring-3 md:text-sm';
</script>

<div class="grid gap-2">
	<Label for="title">Title</Label>
	<Input id="title" name="title" type="text" value={values.title} required aria-invalid={errors.title ? true : undefined} />
	{#if errors.title}<p class="text-destructive text-sm">{errors.title}</p>{/if}
</div>

<div class="grid gap-2">
	<div class="flex items-center gap-2">
		<input id="done" name="done" type="checkbox" checked={values.done} class="accent-primary size-4" />
		<Label for="done">Done</Label>
	</div>
	{#if errors.done}<p class="text-destructive text-sm">{errors.done}</p>{/if}
</div>

<div class="grid gap-2">
	<Label for="due">Due</Label>
	<Input id="due" name="due" type="date" value={values.due} aria-invalid={errors.due ? true : undefined} />
	{#if errors.due}<p class="text-destructive text-sm">{errors.due}</p>{/if}
</div>

<div class="grid gap-2">
	<Label for="priority">Priority</Label>
	<select id="priority" name="priority" class={control} required aria-invalid={errors.priority ? true : undefined}>
		<option value="low" selected={values.priority === 'low'}>low</option>
		<option value="normal" selected={values.priority === 'normal'}>normal</option>
		<option value="high" selected={values.priority === 'high'}>high</option>
	</select>
	{#if errors.priority}<p class="text-destructive text-sm">{errors.priority}</p>{/if}
</div>
