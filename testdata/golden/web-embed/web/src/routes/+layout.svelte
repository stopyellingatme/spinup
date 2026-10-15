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
				<div class="flex items-center gap-3">
					<a href="/account" class="text-muted-foreground text-sm hover:underline" data-testid="user-email">{data.user.email}</a>

				</div>

			{/if}
		</nav>
	</header>
	<main class="mx-auto w-full max-w-5xl flex-1 px-4 py-8">
		{@render children()}
	</main>
</div>
