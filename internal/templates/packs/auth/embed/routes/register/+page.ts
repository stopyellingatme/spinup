import { requireGuest } from '#lib/auth.js';
import type { PageLoad } from './$types';

// A signed-in visitor goes home. Otherwise the page gets the sanitized return path. The root load
// has already read the session from the Go API.
export const load: PageLoad = async ({ parent, url }) => {
	const { user } = await parent();
	return { redirectTo: requireGuest(user, url) };
};
