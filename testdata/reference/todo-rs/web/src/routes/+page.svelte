<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
</script>

<Card.Root class="mx-auto w-full max-w-lg">
	<Card.Header>
		<Card.Title>
			<h1 class="text-lg font-semibold tracking-tight">Welcome to demo</h1>
		</Card.Title>
		<Card.Description>
			{#if data.user}
				You are signed in as {data.user.email}.
			{:else}
				Sign in or create an account to continue.
			{/if}
		</Card.Description>
	</Card.Header>
	{#if data.user}
		<Card.Content class="text-sm">
			<p>Run <code class="font-mono text-xs">spinup g resource</code> to add a typed API and its pages.</p>
		</Card.Content>
		<Card.Footer class="gap-2">
			<Button href="/api/docs" variant="outline" data-sveltekit-reload>API docs</Button>
		</Card.Footer>
	{:else}
		<Card.Footer class="gap-2">
			<Button href="/login">Sign in</Button>
			<Button href="/register" variant="outline">Create account</Button>
		</Card.Footer>
	{/if}
</Card.Root>
