<script lang="ts">
	import '../app.css';
	import type { LayoutProps } from './$types';

	let { data, children }: LayoutProps = $props();
</script>

<svelte:head>
	<title>todo</title>
</svelte:head>

<div class="flex min-h-screen flex-col">
	<header class="border-b">
		<nav class="mx-auto flex h-14 w-full max-w-5xl items-center justify-between px-4">
			<a href="/" class="font-semibold">todo</a>
			{#if data.user}
				<span class="text-muted-foreground text-sm" data-testid="user-email">{data.user.email}</span>
			{:else}
				<a href="/login" class="text-sm font-medium hover:underline">Sign in</a>
			{/if}
		</nav>
	</header>
	<main class="mx-auto w-full max-w-5xl flex-1 px-4 py-8">
		{@render children()}
	</main>
</div>
