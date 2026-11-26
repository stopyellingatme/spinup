<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { session } from '#lib/session.svelte.js';
	import { signOut } from '#lib/sign-out.js';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
	let failed = $state(false);

	// signOut calls the Go API and shows the reason in a toast when it fails. A failed sign-out
	// keeps the session, so the user can try again.
	async function leave() {
		failed = false;
		failed = !(await signOut());
	}
</script>

<svelte:head>
	<title>Account</title>
</svelte:head>

<Card.Root class="mx-auto w-full max-w-sm">
	<Card.Header>
		<Card.Title>Account</Card.Title>
		<Card.Description>You are signed in.</Card.Description>
	</Card.Header>
	<Card.Content class="grid gap-4">
		<p class="text-sm">
			Email: <span class="font-medium" data-testid="account-email">{data.user.email}</span>
		</p>
		{#if failed}
			<p class="text-destructive text-sm" role="alert" data-testid="sign-out-error">Sign-out failed. Try again.</p>
		{/if}
	</Card.Content>
	<Card.Footer>
		<Button variant="outline" disabled={session.signingOut} onclick={leave}>Sign out</Button>
	</Card.Footer>
</Card.Root>
