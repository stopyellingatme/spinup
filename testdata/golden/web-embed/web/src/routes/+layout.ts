import type { LayoutLoad } from './$types';

export const ssr = false;

// The browser sends the session cookie to the API on the same origin.
export const load: LayoutLoad = async ({ fetch }) => {
	try {
		const response = await fetch('/api/session');
		if (response.ok) {
			const session: { user: App.User | null } = await response.json();
			return { user: session.user };
		}
	} catch {
		// The API can be unavailable during development.
	}
	return { user: null as App.User | null };
};
