import { redirect } from '@sveltejs/kit';
import { SESSION_COOKIE, postAuth } from '#lib/server/auth.js';
import type { Actions, PageServerLoad } from './$types';

// Signing out needs a POST from a form. A GET (a link or an image) cannot sign anyone out.
export const load: PageServerLoad = () => {
	redirect(303, '/');
};

export const actions = {
	default: async ({ fetch, cookies, url }) => {
		const result = await postAuth(fetch, cookies, '/api/auth/logout');
		if (!result.ok) {
			// The API did not clear the cookie. Clear it here, so the browser is signed out.
			console.warn(`logout: the API answered ${result.status}`);
			cookies.delete(SESSION_COOKIE, {
				path: '/',
				httpOnly: true,
				sameSite: 'lax',
				secure: url.protocol === 'https:'
			});
		}
		redirect(303, '/');
	}
} satisfies Actions;
