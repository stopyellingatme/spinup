<script lang="ts">
	import { defaults, setError, setMessage, superForm, type ErrorStatus } from 'sveltekit-superforms';
	import { zod4 } from 'sveltekit-superforms/adapters';
	import { postAuth } from '#lib/auth.js';
	import { resetRequestSchema } from './schema';

	let sent = $state(false);
	const adapter = zod4(resetRequestSchema);
	// The browser posts to /api/auth/reset/request. The answer is the same for every address.
	const { form, errors, message, submitting, enhance } = superForm(defaults(adapter), {
		SPA: true,
		id: '/api/auth/reset/request',
		validators: adapter,
		resetForm: false,
		async onUpdate({ form }) {
			if (!form.valid) return;
			const result = await postAuth(globalThis.fetch, '/api/auth/reset/request', { email: form.data.email });
			if (result.ok) {
				sent = true;
				return;
			}
			const status = result.status as ErrorStatus;
			if (result.errors.email) setError(form, 'email', result.errors.email, { status });
			if (result.errors.form) setMessage(form, result.errors.form, { status });
		}
	});
</script>

<svelte:head><title>Reset your password</title><meta name="referrer" content="no-referrer" /></svelte:head>

<section class="mx-auto grid w-full max-w-sm gap-4">
	<h1 class="text-2xl font-semibold">Reset your password</h1>
	{#if sent}
		<p role="status">If an account exists for this email address, you will receive a reset link.</p>
	{:else}
		<form method="POST" use:enhance class="grid gap-4">
			{#if $message}<p role="alert" data-testid="form-error">{$message}</p>{/if}
			<label for="email">Email</label>
			<input
				class="rounded-md border p-2"
				id="email"
				name="email"
				type="email"
				autocomplete="email"
				required
				bind:value={$form.email}
				aria-invalid={$errors.email ? true : undefined}
				aria-describedby={$errors.email ? 'email-error' : undefined}
			/>
			{#if $errors.email}<p role="alert" id="email-error">{$errors.email[0]}</p>{/if}
			<button class="rounded-md border p-2" type="submit" disabled={$submitting}>Send a reset link</button>
		</form>
	{/if}
	<a href="/login">Return to sign in</a>
</section>
