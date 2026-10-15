import { fail, redirect } from '@sveltejs/kit';
import { postAuth } from '#lib/server/auth.js';
import type { Actions, PageServerLoad } from './$types';

export const load: PageServerLoad = ({ locals }) => {
	if (locals.user) redirect(303, '/account');
};

export const actions = {
	// The Go API checks the password, rotates the session token, and sets the cookie.
	default: async ({ request, fetch, cookies }) => {
		const form = await request.formData();
		const email = String(form.get('email') ?? '');
		const password = String(form.get('password') ?? '');
		const result = await postAuth(fetch, cookies, '/api/auth/login', { email, password });
		// Never send the password back to the page.
		if (!result.ok) return fail(result.status, { email, errors: result.errors });
		redirect(303, '/');
	}
} satisfies Actions;
