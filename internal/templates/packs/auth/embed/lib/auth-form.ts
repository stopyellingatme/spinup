// Managed by spinup: `spinup upgrade` keeps this file current.
// The form setup of the sign-in and register pages in embed mode (ADR 0039). The browser checks the
// values with the zod schema, posts them to the Go API, and shows the problem details of the answer
// at the field or as the form message. The Go API sets the HttpOnly session cookie.
import { defaults, setError, setMessage, superForm, type ErrorStatus } from 'sveltekit-superforms';
import { zod4 } from 'sveltekit-superforms/adapters';
import type { z } from 'zod';
import { postAuth } from '#lib/auth.js';
import { announceSessionChange } from '#lib/session-watch.js';
import { leaveSession } from '#lib/session.svelte.js';

type AuthSchema = z.ZodObject<{ email: z.ZodString; password: z.ZodString }>;

/**
 * authForm returns a superForm for an auth endpoint. On success it tells the other tabs, then
 * replaces the document with the path that redirectTo returns. The new document reads the session
 * again, so no page data of the signed-out visitor stays. Call it while a component starts.
 *
 * The form posts with the plain fetch, not with observedFetch: a 401 of the sign-in endpoint is the
 * answer for a wrong password, and it must not start a session check.
 */
export function authForm(schema: AuthSchema, path: '/api/auth/login' | '/api/auth/register', redirectTo: () => string) {
	const adapter = zod4(schema);
	return superForm(defaults(adapter), {
		SPA: true,
		// One id for each endpoint. Without it, superforms warns about a duplicate id when the
		// sign-in page mounts a second time in one tab.
		id: path,
		validators: adapter,
		// Keep the values in the fields after a failed post, including the password.
		resetForm: false,
		async onUpdate({ form }) {
			if (!form.valid) return;
			const result = await postAuth(globalThis.fetch, path, { email: form.data.email, password: form.data.password });
			if (!result.ok) {
				const status = result.status as ErrorStatus;
				if (result.errors.email) setError(form, 'email', result.errors.email, { status });
				if (result.errors.password) setError(form, 'password', result.errors.password, { status });
				if (result.errors.form) setMessage(form, result.errors.form, { status });
				return;
			}
			announceSessionChange();
			// The page load sanitized the target. leaveSession checks it again.
			leaveSession(redirectTo());
		}
	});
}
