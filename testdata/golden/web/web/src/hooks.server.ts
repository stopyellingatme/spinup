import type { Handle, HandleFetch } from '@sveltejs/kit/hooks';
import { API_URL } from '$app/env/private';

// The Go API, without a trailing slash.
const api = API_URL.replace(/\/+$/, '');
// Server-side fetches to this prefix go to the Go API. It is the same origin as paths.origin.
const publicAPI = `${__PUBLIC_ORIGIN__}/api/`;

/** loadUser asks the Go API who owns the session cookie. It never throws. */
async function loadUser(cookie: string | null): Promise<App.User | null> {
	if (!cookie) return null;
	try {
		const res = await fetch(`${api}/api/session`, {
			headers: { cookie, accept: 'application/json' }
		});
		if (res.status === 401 || res.status === 404) return null;
		if (!res.ok) {
			console.warn(`session: GET ${api}/api/session returned ${res.status}`);
			return null;
		}
		const body = (await res.json()) as { user?: App.User };
		return body.user ?? null;
	} catch (err) {
		console.error(`session: cannot reach the API at ${api}:`, err);
		return null;
	}
}

export const handle: Handle = async ({ event, resolve }) => {
	event.locals.user =
		event.url.pathname === '/healthz' ? null : await loadUser(event.request.headers.get('cookie'));
	return resolve(event);
};

// Rewrite `${PUBLIC_ORIGIN}/api/...` to the Go API, and send the browser cookie only there.
export const handleFetch: HandleFetch = async ({ event, request, fetch }) => {
	if (request.url.startsWith(publicAPI)) {
		request = new Request(api + request.url.slice(__PUBLIC_ORIGIN__.length), request);
		const cookie = event.request.headers.get('cookie');
		if (cookie) request.headers.set('cookie', cookie);
	}
	return fetch(request);
};
