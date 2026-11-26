import { requireGuest } from '#lib/auth.js';
import type { PageLoad } from './$types';

// A signed-in visitor goes home. Otherwise the page gets the sanitized return path. The root load
// has already read the session from the Go API. A limited OIDC callback returns here with
// ?error=rate_limited (kit auth).
export const load: PageLoad = async ({ parent, url }) => {
	const { user } = await parent();
	const redirectTo = requireGuest(user, url);
	return { redirectTo, rateLimited: url.searchParams.get('error') === 'rate_limited' };
};
