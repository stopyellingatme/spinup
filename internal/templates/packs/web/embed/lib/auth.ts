// Managed by spinup: `spinup upgrade` keeps this file current.
// The browser auth helpers of embed mode (ADR 0039): the page guards, the return path, the calls
// to the Go auth endpoints, and the session and flags reads of the root load. The Go API sets and
// clears the HttpOnly session cookie. No code here reads or writes a cookie.
import { error, redirect } from '@sveltejs/kit';
import { home } from '#lib/nav.gen.js';
// The two modules import each other. Each one uses the other only inside function bodies.
import { requireVerified } from '#lib/session.svelte.js';

/** The dependency key of the session read. The session watch invalidates it. */
export const SESSION_DEP = 'app:session';

/** The dependency key of the public flags. Invalidate it after an admin saves a flag. */
export const FLAGS_DEP = 'app:flags';

/** Messages for the fields of an auth form, and for the form as a whole. */
export interface AuthErrors {
	email?: string;
	password?: string;
	form?: string;
}

/** The result of postAuth: ok, or the HTTP status and the messages to show. */
export type AuthResult = { ok: true } | { ok: false; status: number; errors: AuthErrors };

interface Problem {
	detail?: string;
	errors?: { field?: string; message?: string }[];
}

/** Return a same-site path, or fallback. Validate raw and decoded paths like kit auth OIDC. */
export function safeRedirect(target: string | null, fallback: string): string {
	if (!target) return fallback;
	try {
		const path = decodeURIComponent(target.split(/[?#]/, 1)[0]);
		for (const value of [target, path]) {
			if (!value.startsWith('/') || value.startsWith('//') || /[\\\r\n\0\t]/.test(value)) {
				return fallback;
			}
		}
	} catch {
		return fallback;
	}
	return target;
}

/**
 * loginHref returns the sign-in address that returns to url. It never wraps the sign-in or the
 * register page in a second redirectTo: parallel loads can call it with the sign-in address.
 */
export function loginHref(url: URL): string {
	if (url.pathname === '/login' || url.pathname === '/register') {
		return '/login' + (url.searchParams.has('redirectTo') ? url.search : '');
	}
	return '/login?redirectTo=' + encodeURIComponent(url.pathname + url.search);
}

/**
 * Require a signed-in user, or redirect to sign-in with the current path and query. While the last
 * session read failed, it throws a 503 error instead, so no redirect drops the deep link.
 */
export function requireUser(user: App.User | null, url: URL): App.User {
	requireVerified();
	if (!user) redirect(303, loginHref(url));
	return user;
}

/** Require a signed-in user with a listed role, or show the access error. It checks like requireUser. */
export function requireRole(user: App.User | null, url: URL, ...roles: string[]): App.User {
	const signedIn = requireUser(user, url);
	if (!roles.includes(signedIn.role)) {
		error(403, 'Your role cannot open this page. Try: sign in with an account that has a required role.');
	}
	return signedIn;
}

/**
 * Send a signed-in visitor home. Otherwise return the safe return path of the sign-in pages. While
 * the last session read failed, it throws a 503 error instead.
 */
export function requireGuest(user: App.User | null, url: URL): string {
	requireVerified();
	if (user) redirect(303, home);
	return safeRedirect(url.searchParams.get('redirectTo'), home);
}

/**
 * postAuth posts to a Go auth endpoint on the same origin, for example `/api/auth/login`. The
 * browser sends and stores the session cookie. Problem details become messages. It never throws.
 */
export async function postAuth(
	fetch: typeof globalThis.fetch,
	path: `/api/${string}`,
	body?: Record<string, string>
): Promise<AuthResult> {
	let res: Response;
	try {
		res = await fetch(path, {
			method: 'POST',
			credentials: 'same-origin',
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
	if (res.ok) return { ok: true };
	const status = res.status >= 400 && res.status <= 599 ? res.status : 502;
	return { ok: false, status, errors: await problemErrors(res, status, path) };
}

async function problemErrors(res: Response, status: number, path: string): Promise<AuthErrors> {
	// A 503 is a busy API (every password hash slot is taken). Its detail tells the user to retry.
	if (status >= 500 && status !== 503) {
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
	for (const e of Array.isArray(problem?.errors) ? problem.errors : []) {
		if ((e?.field === 'email' || e?.field === 'password') && typeof e.message === 'string' && e.message && !errors[e.field]) {
			errors[e.field] = e.message;
		}
	}
	if (!errors.email && !errors.password) {
		const detail = typeof problem?.detail === 'string' && problem.detail ? problem.detail : undefined;
		errors.form = detail ?? (status === 503 ? 'The server is busy. Try again in a moment.' : `The request failed (${status}).`);
	}
	return errors;
}

/**
 * loadSession asks the Go API who owns the session cookie. Only a 401 means signed out. Any other
 * failure throws a 503 error, so the root load fails closed and shows no protected content.
 */
export async function loadSession(fetch: typeof globalThis.fetch): Promise<App.User | null> {
	let res: Response;
	try {
		res = await fetch('/api/session', { credentials: 'same-origin', headers: { accept: 'application/json' } });
	} catch (err) {
		console.error('session: cannot reach the API:', err);
		error(503, 'The server is not available. Try again later.');
	}
	// The browser already reports the 401 in its console. It is the normal answer for a visitor.
	if (res.status === 401) return null;
	if (!res.ok) {
		console.error(`session: GET /api/session returned ${res.status}`);
		error(503, 'The session cannot be verified. Try again later.');
	}
	const user = sessionUser(await res.json().catch(() => undefined));
	if (!user) {
		console.error('session: GET /api/session returned an answer without a user');
		error(503, 'The session cannot be verified. Try again later.');
	}
	return user;
}

// sessionUser copies the three known fields of {user:{id,email,role}}, or returns null.
function sessionUser(body: unknown): App.User | null {
	const user = (body as { user?: Partial<App.User> } | null | undefined)?.user;
	if (!user || typeof user.id !== 'number' || typeof user.email !== 'string' || typeof user.role !== 'string') {
		return null;
	}
	return { id: user.id, email: user.email, role: user.role };
}

/**
 * loadFlags reads the public flags of this visitor from the Go API. The API evaluates every rule.
 * It never throws: a failure warns and gives an empty map, so each flag is off.
 */
export async function loadFlags(fetch: typeof globalThis.fetch): Promise<Record<string, boolean>> {
	try {
		const res = await fetch('/api/flags', { credentials: 'same-origin', headers: { accept: 'application/json' } });
		if (!res.ok) {
			console.warn(`flags: GET /api/flags returned ${res.status}`);
			return {};
		}
		const body = (await res.json()) as { flags?: unknown };
		const flags: Record<string, boolean> = {};
		if (body?.flags && typeof body.flags === 'object') {
			for (const [key, value] of Object.entries(body.flags)) {
				if (typeof value === 'boolean') flags[key] = value;
			}
		}
		return flags;
	} catch (err) {
		console.warn('flags: cannot read the public flags:', err);
		return {};
	}
}
