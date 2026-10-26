<script lang="ts">
	import type { Snippet } from 'svelte';
	import { cn } from '#lib/utils.js';

	let {
		title,
		description,
		actions,
		class: className
	}: {
		/** The page heading (h1). */
		title: string;
		/** One short sentence below the heading. */
		description?: string;
		/** Buttons or links at the right end, for example "New". */
		actions?: Snippet;
		class?: string;
	} = $props();
</script>

<div class={cn('mb-6 flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between', className)}>
	<div class="grid min-w-0 gap-1">
		<h1 class="text-2xl font-semibold tracking-tight">{title}</h1>
		{#if description}
			<p class="text-muted-foreground text-sm">{description}</p>
		{/if}
	</div>
	{#if actions}
		<div class="flex shrink-0 flex-wrap items-center gap-2">
			{@render actions()}
		</div>
	{/if}
</div>
