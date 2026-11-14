<script lang="ts">
	import { page } from '$app/state';
	import { Button } from '#lib/components/ui/button/index.js';
	import { home } from '#lib/nav.gen.js';

	const title = $derived(
		page.status === 404
			? 'Page not found'
			: page.status === 403
				? 'Access denied'
				: page.status >= 500
					? 'Something went wrong'
					: 'Request failed'
	);
	// SvelteKit's default messages repeat the title. Show only a message that adds something.
	const detail = $derived(
		page.error?.message && !['Not Found', 'Internal Error'].includes(page.error.message)
			? page.error.message
			: ''
	);
</script>

<svelte:head>
	<title>{page.status} {title}</title>
</svelte:head>

<div class="mx-auto flex max-w-md flex-col items-center gap-4 py-16 text-center">
	<p class="text-muted-foreground font-mono text-sm font-medium">{page.status}</p>
	<h1 class="text-2xl font-semibold tracking-tight">{title}</h1>
	{#if detail}
		<p class="text-muted-foreground text-sm">{detail}</p>
	{/if}
	<Button href={home} class="mt-2">Go to the home page</Button>
</div>
