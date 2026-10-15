// Managed by spinup: `spinup upgrade` keeps this file current.
// Form actions call the Go auth endpoints with this helper (kit auth, ADR 0008 §3).
import type { Cookies } from '@sveltejs/kit';

/** The name of the Go session cookie. */
export const SESSION_COOKIE = 'session';

/** Messages for the fields of an auth form, and for the form as a whole. */
export interface AuthErrors {
	email?: string;
	password?: string;
	form?: string;
}

/** The result of postAuth: ok, or an HTTP status for fail() and the messages to show. */
export type AuthResult = { ok: true } | { ok: false; status: number; errors: AuthErrors };

interface Problem {
	detail?: string;
	errors?: { field?: string; message?: string }[];
}

/**
 * postAuth posts to a Go auth endpoint, for example `/api/auth/login`.
 *
 * Pass the `fetch` of the action: handleFetch in hooks.server.ts sends the request to API_URL
 * with the browser cookie. postAuth copies the session cookie of the answer to the browser, and
 * turns problem details into messages. It never throws.
 */
export async function postAuth(
	fetch: typeof globalThis.fetch,
	cookies: Cookies,
	path: `/api/${string}`,
	body?: Record<string, string>
): Promise<AuthResult> {
	let res: Response;
	try {
		res = await fetch(`${__PUBLIC_ORIGIN__}${path}`, {
			method: 'POST',
			headers:
				body === undefined
					? { accept: 'application/json' }
					: { accept: 'application/json', 'content-type': 'application/json' },
			body: body === undefined ? undefined : JSON.stringify(body)
		});
	} catch (err) {
		console.error(`auth: cannot reach the API for POST ${path}:`, err);
		return { ok: false, status: 503, errors: { form: 'The server is not available. Try again later.' } };
	}
	forwardSessionCookie(res, cookies);
	if (res.ok) return { ok: true };
	const status = res.status >= 400 && res.status <= 599 ? res.status : 502;
	return { ok: false, status, errors: await problemErrors(res, path) };
}

/**
 * forwardSessionCookie copies the Go session cookie from res to the browser (ADR 0002 rule 7).
 * The Set-Cookie header of a server-side fetch does not reach the browser by itself. Other
 * cookies are ignored. An empty or expired session cookie deletes the browser cookie.
 */
export function forwardSessionCookie(res: Response, cookies: Cookies): void {
	for (const header of res.headers.getSetCookie()) {
		const { name, value, path, maxAge, httpOnly, secure, sameSite } = cookies.parse(header);
		if (name !== SESSION_COOKIE) continue;
		const options = {
			path: path ?? '/',
			httpOnly: httpOnly ?? true,
			secure: secure ?? false,
			sameSite: sameSite ?? 'lax'
		};
		if (!value || (maxAge !== undefined && maxAge <= 0)) {
			cookies.delete(name, options);
		} else {
			cookies.set(name, value, { ...options, maxAge });
		}
	}
}

async function problemErrors(res: Response, path: string): Promise<AuthErrors> {
	if (res.status >= 500) {
		console.error(`auth: POST ${path} returned ${res.status}`);
		return { form: 'Something went wrong on the server. Try again later.' };
	}
	let problem: Problem = {};
	try {
		problem = (await res.json()) as Problem;
	} catch {
		// Not problem details: use the status text below.
	}
	const errors: AuthErrors = {};
	for (const e of problem.errors ?? []) {
		if ((e.field === 'email' || e.field === 'password') && e.message && !errors[e.field]) {
			errors[e.field] = e.message;
		}
	}
	if (!errors.email && !errors.password) {
		errors.form = problem.detail ?? `The request failed (${res.status}).`;
	}
	return errors;
}
