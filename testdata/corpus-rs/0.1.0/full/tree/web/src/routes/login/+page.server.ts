import { fail, redirect } from '@sveltejs/kit';
import { postAuth, safeRedirect } from '#lib/server/auth.js';
import { home } from '#lib/nav.gen.js';
import type { Actions, PageServerLoad } from './$types';

export const load: PageServerLoad = ({ locals, url }) => {
	if (locals.user) redirect(303, home);
	return { redirectTo: safeRedirect(url.searchParams.get('redirectTo'), home) };
};

export const actions = {
	// The Go API checks the password, rotates the session token, and sets the cookie.
	default: async ({ request, fetch, cookies, url }) => {
		const form = await request.formData();
		const email = String(form.get('email') ?? '');
		const password = String(form.get('password') ?? '');
		const result = await postAuth(fetch, cookies, '/api/auth/login', { email, password });
		// Never send the password back to the page.
		if (!result.ok) return fail(result.status, { email, errors: result.errors });
		redirect(303, safeRedirect(url.searchParams.get('redirectTo'), home));
	}
} satisfies Actions;
