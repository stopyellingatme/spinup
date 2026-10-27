<script lang="ts">
	import { enhance } from '$app/forms';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();
	let pending = $state(false);
	const redirectQuery = $derived('?redirectTo=' + encodeURIComponent(data.redirectTo));
</script>

<svelte:head>
	<title>Sign in</title>
</svelte:head>

<Card.Root class="mx-auto w-full max-w-sm">
	<Card.Header>
		<Card.Title>Sign in</Card.Title>
		<Card.Description>Enter your email address and password.</Card.Description>
	</Card.Header>
	<Card.Content>
		<form
			method="POST"
			action={`/login${redirectQuery}`}
			class="grid gap-4"
			use:enhance={() => {
				pending = true;
				return async ({ update }) => {
					await update();
					pending = false;
				};
			}}
		>
			{#if form?.errors.form}
				<p class="text-destructive text-sm" role="alert" data-testid="form-error">{form.errors.form}</p>
			{/if}
			<div class="grid gap-2">
				<Label for="email">Email</Label>
				<Input
					id="email"
					name="email"
					type="email"
					autocomplete="email"
					required
					value={form?.email ?? ''}
					aria-invalid={form?.errors.email ? true : undefined}
					aria-describedby={form?.errors.email ? 'email-error' : undefined}
				/>
				{#if form?.errors.email}
					<p id="email-error" class="text-destructive text-sm">{form.errors.email}</p>
				{/if}
			</div>
			<div class="grid gap-2">
				<Label for="password">Password</Label>
				<Input
					id="password"
					name="password"
					type="password"
					autocomplete="current-password"
					required
					maxlength={512}
					aria-invalid={form?.errors.password ? true : undefined}
					aria-describedby={form?.errors.password ? 'password-error' : undefined}
				/>
				{#if form?.errors.password}
					<p id="password-error" class="text-destructive text-sm">{form.errors.password}</p>
				{/if}
			</div>
			<Button type="submit" disabled={pending}>Sign in</Button>
		</form>
		<a href="/reset" class="mt-4 block text-sm hover:underline">Forgot password?</a>
	</Card.Content>
	<Card.Footer class="text-sm">
		<span class="text-muted-foreground">No account?</span>
		<a href={`/register${redirectQuery}`} class="ml-1 font-medium hover:underline">Create one</a>
	</Card.Footer>
</Card.Root>
