<script lang="ts" module>
	/** What the children snippet of Field gets. Put these attributes on the control. */
	export type FieldControl = {
		/** The id of the control. The label points to it. */
		id: string;
		/** The ids of the description and the error, or undefined. Use it as aria-describedby. */
		describedBy: string | undefined;
		/** True when the field has errors. Use it as aria-invalid. */
		invalid: boolean;
		/** The same values as HTML attributes, ready to spread: <Input {...props} />. */
		props: {
			id: string;
			name: string;
			'aria-describedby': string | undefined;
			'aria-invalid': true | undefined;
		};
	};
</script>

<script lang="ts">
	import type { Snippet } from 'svelte';
	import { Label } from '#lib/components/ui/label/index.js';
	import { cn } from '#lib/utils.js';

	let {
		label,
		name,
		id: idProp,
		errors,
		description,
		class: className,
		children
	}: {
		/** The visible label. */
		label: string;
		/** The form field name. */
		name: string;
		/** The id of the control. The default is field-<name>. */
		id?: string;
		/** The validation messages, for example $errors.title from superforms. */
		errors?: string[] | null;
		/** A hint below the control. */
		description?: string;
		class?: string;
		/** The control. Spread props on it, or use id, describedBy, and invalid. */
		children: Snippet<[FieldControl]>;
	} = $props();

	const id = $derived(idProp ?? `field-${name}`);
	const invalid = $derived((errors?.length ?? 0) > 0);
	const describedBy = $derived(
		[description ? `${id}-description` : '', invalid ? `${id}-error` : ''].filter(Boolean).join(' ') ||
			undefined
	);
	const control: FieldControl = $derived({
		id,
		describedBy,
		invalid,
		props: {
			id,
			name,
			'aria-describedby': describedBy,
			'aria-invalid': invalid ? true : undefined
		}
	});
</script>

<div class={cn('grid gap-2', className)}>
	<Label for={id} class={invalid ? 'text-destructive' : undefined}>{label}</Label>
	{@render children(control)}
	{#if description}
		<p id="{id}-description" class="text-muted-foreground text-sm">{description}</p>
	{/if}
	{#if invalid}
		<p id="{id}-error" class="text-destructive text-sm">{errors?.join(' ')}</p>
	{/if}
</div>
