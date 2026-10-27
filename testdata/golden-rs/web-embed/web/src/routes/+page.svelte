<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { nav } from '#lib/nav.gen.js';
</script>

<Card.Root class="mx-auto w-full max-w-lg">
	<Card.Header>
		<Card.Title>
			<h1 class="text-lg font-semibold tracking-tight">Welcome to todo</h1>
		</Card.Title>
		<Card.Description>
			A SvelteKit app on the same origin as its API.
		</Card.Description>
	</Card.Header>
	<Card.Content class="text-sm">
		{#if nav.length > 0}
			<ul class="grid gap-1" aria-label="Pages">
				{#each nav as item (item.href)}
					<li>
						<a href={item.href} class="font-medium underline-offset-4 hover:underline">{item.label}</a>
					</li>
				{/each}
			</ul>
		{:else}
			<p>Run <code class="font-mono text-xs">spinup g resource</code> to add a typed API and its pages.</p>
		{/if}
	</Card.Content>
	<Card.Footer class="gap-2">
		<Button href="/api/docs" variant="outline" data-sveltekit-reload>API docs</Button>
	</Card.Footer>
</Card.Root>
