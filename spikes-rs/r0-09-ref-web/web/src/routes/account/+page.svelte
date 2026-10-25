<script lang="ts">
	import { enhance } from '$app/forms';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
</script>

<svelte:head>
	<title>Account</title>
</svelte:head>

<Card.Root class="mx-auto w-full max-w-sm">
	<Card.Header>
		<Card.Title>Account</Card.Title>
		<Card.Description>You are signed in.</Card.Description>
	</Card.Header>
	<Card.Content>
		<p class="text-sm">
			Email: <span class="font-medium" data-testid="account-email">{data.user?.email}</span>
		</p>
	</Card.Content>
	<Card.Footer>
		<form method="POST" action="/logout" use:enhance>
			<Button type="submit" variant="outline">Sign out</Button>
		</form>
	</Card.Footer>
</Card.Root>
