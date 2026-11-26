<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import type { PageProps } from './$types';

	import { authForm } from '#lib/auth-form.js';
	import { registerSchema } from '../login/schema';

	let { data }: PageProps = $props();
	// The browser posts to /api/auth/register. The Go API sets the session cookie.
	const { form, errors, message, submitting, enhance } = authForm(registerSchema, '/api/auth/register', () => data.redirectTo);
	const redirectQuery = $derived('?redirectTo=' + encodeURIComponent(data.redirectTo));
</script>

<svelte:head>
	<title>Create an account</title>
</svelte:head>

<Card.Root class="mx-auto w-full max-w-sm">
	<Card.Header>
		<Card.Title>Create an account</Card.Title>
		<Card.Description>Use at least 12 characters for your password.</Card.Description>
	</Card.Header>
	<Card.Content>
		<form method="POST" class="grid gap-4" use:enhance>
			{#if $message}
				<p class="text-destructive text-sm" role="alert" data-testid="form-error">{$message}</p>
			{/if}
			<div class="grid gap-2">
				<Label for="email">Email</Label>
				<Input
					id="email"
					name="email"
					type="email"
					autocomplete="email"
					required
					bind:value={$form.email}
					aria-invalid={$errors.email ? true : undefined}
					aria-describedby={$errors.email ? 'email-error' : undefined}
				/>
				{#if $errors.email}
					<p id="email-error" class="text-destructive text-sm">{$errors.email[0]}</p>
				{/if}
			</div>
			<div class="grid gap-2">
				<Label for="password">Password</Label>
				<Input
					id="password"
					name="password"
					type="password"
					autocomplete="new-password"
					required
					minlength={12}
					maxlength={512}
					bind:value={$form.password}
					aria-invalid={$errors.password ? true : undefined}
					aria-describedby={$errors.password ? 'password-error' : undefined}
				/>
				{#if $errors.password}
					<p id="password-error" class="text-destructive text-sm">{$errors.password[0]}</p>
				{/if}
			</div>
			<Button type="submit" disabled={$submitting}>Create account</Button>
		</form>
	</Card.Content>
	<Card.Footer class="text-sm">
		<span class="text-muted-foreground">Already registered?</span>
		<a href={`/login${redirectQuery}`} class="ml-1 font-medium hover:underline">Sign in</a>
	</Card.Footer>
</Card.Root>
