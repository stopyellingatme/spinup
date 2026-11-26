import { requireUser } from '#lib/auth.js';
import type { PageLoad } from './$types';

// The root load has read the session from the Go API. A signed-out visitor goes to sign-in.
export const load: PageLoad = async ({ parent, url }) => {
	const { user } = await parent();
	return { user: requireUser(user, url) };
};
