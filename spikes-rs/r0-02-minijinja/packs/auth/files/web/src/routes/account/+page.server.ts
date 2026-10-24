import { requireUser } from '#lib/server/auth.js';
import type { PageServerLoad } from './$types';

// hooks.server.ts loads the user from the Go session (GET /api/session) on every request.
export const load: PageServerLoad = ({ locals, url }) => {
	return { user: requireUser(locals, url) };
};
