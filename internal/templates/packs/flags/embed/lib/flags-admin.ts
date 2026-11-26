// Managed by spinup: `spinup upgrade` keeps this file current.
// The admin page of embed mode calls the flag admin endpoints of the Go API from the browser with
// these helpers (ADR 0029, ADR 0039). The Go API checks the admin role on every call. Node mode
// keeps the same helpers in lib/server/flags.ts.
import { observedFetch } from '#lib/session-watch.js';

/** FLAGS_ADMIN_DEP is the dependency key of the admin flags load. A save invalidates it. */
export const FLAGS_ADMIN_DEP = 'app:flags-admin';

/** UserRule is an explicit on or off rule for one account. */
export interface UserRule {
	user_id: number;
	email: string;
	enabled: boolean;
}

/** FlagEntry is one flag with its declaration, its stored rule, and its status. */
export interface FlagEntry {
	key: string;
	description: string;
	default: boolean;
	public: boolean;
	expires?: string;
	enabled: boolean;
	roles: string[];
	percentage: number;
	users: UserRule[];
	stale: boolean;
	expired: boolean;
	updated_at: string;
}

/** FlagsAdmin is the answer of GET /api/admin/flags. */
export interface FlagsAdmin {
	flags: FlagEntry[];
	roles: string[];
	admin_role: string;
}

/** FlagRule is the body of a rule change. */
export interface FlagRule {
	enabled: boolean;
	roles: string[];
	percentage: number;
}

/** FlagErrors holds the messages for the fields of a flag form, and for the form as a whole. */
export interface FlagErrors {
	fields: Record<string, string>;
	form?: string;
}

/** FlagResult is ok with the data, or an HTTP status and the messages to show. */
export type FlagResult<T> = { ok: true; data: T } | { ok: false; status: number; errors: FlagErrors };

interface Problem {
	detail?: string;
	errors?: { field?: string; message?: string }[];
}

const unavailable = 'The server is not available. Try again later.';
const serverError = 'Something went wrong on the server. Try again later.';
const busy = 'The server is busy. Try again in a moment.';

// call sends one request through observedFetch, so a 401 asks the session watch for a new read.
// It never throws: a transport failure is a 503 result.
async function call<T>(
	fetch: typeof globalThis.fetch,
	method: string,
	path: `/api/${string}`,
	body?: unknown
): Promise<FlagResult<T>> {
	let res: Response;
	try {
		res = await observedFetch(fetch)(path, {
			method,
			headers:
				body === undefined
					? { accept: 'application/json' }
					: { accept: 'application/json', 'content-type': 'application/json' },
			body: body === undefined ? undefined : JSON.stringify(body)
		});
	} catch (err) {
		console.error(`flags: cannot reach the API for ${method} ${path}:`, err);
		return { ok: false, status: 503, errors: { fields: {}, form: unavailable } };
	}
	if (res.ok) {
		if (res.status === 204) return { ok: true, data: undefined as T };
		try {
			return { ok: true, data: (await res.json()) as T };
		} catch (err) {
			console.error(`flags: ${method} ${path} returned a body that is not JSON:`, err);
			return { ok: false, status: 502, errors: { fields: {}, form: serverError } };
		}
	}
	const status = res.status >= 400 && res.status <= 599 ? res.status : 502;
	if (status >= 500 && status !== 503) {
		console.error(`flags: ${method} ${path} returned ${status}`);
		return { ok: false, status, errors: { fields: {}, form: serverError } };
	}
	let problem: Problem = {};
	try {
		problem = (await res.json()) as Problem;
	} catch {
		// Not problem details: use the status text below.
	}
	const errors: FlagErrors = { fields: {} };
	for (const e of problem.errors ?? []) {
		if (e.field && e.message && !errors.fields[e.field]) errors.fields[e.field] = e.message;
	}
	if (Object.keys(errors.fields).length === 0) {
		errors.form = problem.detail ?? (status === 503 ? busy : `The request failed (${status}).`);
	}
	return { ok: false, status, errors };
}

/** listFlags reads every flag with its rules. Pass the fetch of the load. */
export function listFlags(fetch: typeof globalThis.fetch): Promise<FlagResult<FlagsAdmin>> {
	return call<FlagsAdmin>(fetch, 'GET', '/api/admin/flags');
}

/** putRule replaces the global rule of a flag. */
export function putRule(fetch: typeof globalThis.fetch, key: string, rule: FlagRule): Promise<FlagResult<FlagEntry>> {
	return call<FlagEntry>(fetch, 'PUT', `/api/admin/flags/${encodeURIComponent(key)}`, rule);
}

/** putUser sets an on or off rule for the account with this email address. */
export function putUser(
	fetch: typeof globalThis.fetch,
	key: string,
	email: string,
	enabled: boolean
): Promise<FlagResult<FlagEntry>> {
	return call<FlagEntry>(fetch, 'PUT', `/api/admin/flags/${encodeURIComponent(key)}/users/${encodeURIComponent(email)}`, {
		enabled
	});
}

/** clearUser removes the rule of one account. A missing rule is not an error. */
export function clearUser(fetch: typeof globalThis.fetch, key: string, email: string): Promise<FlagResult<void>> {
	return call<void>(fetch, 'DELETE', `/api/admin/flags/${encodeURIComponent(key)}/users/${encodeURIComponent(email)}`);
}
