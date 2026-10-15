import { redirect } from '@sveltejs/kit';
import type { PageServerLoad } from './$types';

// hooks.server.ts loads the user from the Go session (GET /api/session) on every request.
export const load: PageServerLoad = ({ locals }) => {
	if (!locals.user) redirect(303, '/login');
	return { user: locals.user };
};
