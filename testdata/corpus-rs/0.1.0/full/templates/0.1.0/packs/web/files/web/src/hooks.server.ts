import type { Handle, HandleFetch } from '@sveltejs/kit/hooks';
import { API_URL } from '$app/env/private';

// The API, without a trailing slash.
const api = API_URL.replace(/\/+$/, '');
// Server-side fetches to this prefix go to the API. It is the same origin as paths.origin.
const publicAPI = `${__PUBLIC_ORIGIN__}/api/`;

/** loadUser asks the API who owns the session cookie. It never throws. */
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
	const response = await resolve(event);
	if (!response.headers.has('X-Frame-Options')) response.headers.set('X-Frame-Options', 'DENY');
	if (!response.headers.has('Content-Security-Policy')) {
		response.headers.set('Content-Security-Policy', "frame-ancestors 'none'");
	}
	if (event.locals.user) response.headers.set('Cache-Control', 'no-store');
	return response;
};

// The API limits the auth routes for each client. Node is the TCP peer of the API, so this hook
// passes the client address on. ADDRESS_HEADER is the adapter-node setting that names the header
// of the reverse proxy. Without it, getClientAddress() returns the address of the proxy, and all
// users would share one limit. Thus the hook sends the address only when ADDRESS_HEADER is set.
const forwardClient = Boolean(process.env.ADDRESS_HEADER);
let warnedNoAddress = false;

// Rewrite `${PUBLIC_ORIGIN}/api/...` to the API. Send the browser cookie and the client
// address only there. A header from the browser is never passed on.
export const handleFetch: HandleFetch = async ({ event, request, fetch }) => {
	if (request.url.startsWith(publicAPI)) {
		request = new Request(api + request.url.slice(__PUBLIC_ORIGIN__.length), request);
		const cookie = event.request.headers.get('cookie');
		if (cookie) request.headers.set('cookie', cookie);
		request.headers.delete('x-forwarded-for');
		if (forwardClient) {
			try {
				request.headers.set('x-forwarded-for', event.getClientAddress());
			} catch (err) {
				// The request did not come through the proxy, or XFF_DEPTH is too large.
				if (!warnedNoAddress) {
					warnedNoAddress = true;
					console.warn('ratelimit: no client address for the API request, so it has no X-Forwarded-For:', err);
				}
			}
		}
	}
	return fetch(request);
};
