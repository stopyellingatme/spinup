<script lang="ts">
	import { defaults, setError, setMessage, superForm, type ErrorStatus } from 'sveltekit-superforms';
	import { zod4 } from 'sveltekit-superforms/adapters';
	import { postAuth } from '#lib/auth.js';
	import { announceSessionChange } from '#lib/session-watch.js';
	import { leaveSession } from '#lib/session.svelte.js';
	import type { PageProps } from './$types';
	import { resetConfirmSchema } from './schema';

	let { data }: PageProps = $props();
	const adapter = zod4(resetConfirmSchema);
	// The browser posts to /api/auth/reset/confirm. The token goes only in the JSON body.
	const { form, errors, message, submitting, enhance } = superForm(defaults(adapter), {
		SPA: true,
		id: '/api/auth/reset/confirm',
		validators: adapter,
		resetForm: false,
		async onUpdate({ form }) {
			if (!form.valid) return;
			const result = await postAuth(globalThis.fetch, '/api/auth/reset/confirm', {
				token: data.token,
				password: form.data.password
			});
			if (!result.ok) {
				const status = result.status as ErrorStatus;
				if (result.errors.password) setError(form, 'password', result.errors.password, { status });
				if (result.errors.form) setMessage(form, result.errors.form, { status });
				return;
			}
			// The Go API ends a session of this user. Tell the other tabs, then load the sign-in page.
			announceSessionChange();
			leaveSession('/login');
		}
	});
</script>

<svelte:head><title>Choose a new password</title><meta name="referrer" content="no-referrer" /></svelte:head>

<section class="mx-auto grid w-full max-w-sm gap-4">
	<h1 class="text-2xl font-semibold">Choose a new password</h1>
	<form method="POST" use:enhance class="grid gap-4">
		{#if $message}<p role="alert" data-testid="form-error">{$message}</p>{/if}
		<label for="password">New password</label>
		<input
			class="rounded-md border p-2"
			id="password"
			name="password"
			type="password"
			autocomplete="new-password"
			minlength={12}
			maxlength={512}
			required
			bind:value={$form.password}
			aria-invalid={$errors.password ? true : undefined}
			aria-describedby={$errors.password ? 'password-error' : undefined}
		/>
		{#if $errors.password}<p role="alert" id="password-error">{$errors.password[0]}</p>{/if}
		<button class="rounded-md border p-2" type="submit" disabled={$submitting}>Save the new password</button>
	</form>
	<a href="/reset">Request a new link</a>
</section>
