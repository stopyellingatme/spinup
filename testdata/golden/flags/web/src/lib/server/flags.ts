// Managed by spinup: `spinup upgrade` keeps this file current.
// The admin page calls the flag admin endpoints of the Go API with these helpers (ADR 0029).

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

/** Errors holds the messages for the fields of a flag form, and for the form as a whole. */
export interface FlagErrors {
	fields: Record<string, string>;
	form?: string;
}

/** FlagResult is ok with the data, or an HTTP status for fail() and the messages to show. */
export type FlagResult<T> = { ok: true; data: T } | { ok: false; status: number; errors: FlagErrors };

interface Problem {
	detail?: string;
	errors?: { field?: string; message?: string }[];
}

async function call<T>(
	fetch: typeof globalThis.fetch,
	method: string,
	path: string,
	body?: unknown
): Promise<FlagResult<T>> {
	let res: Response;
	try {
		res = await fetch(`${__PUBLIC_ORIGIN__}${path}`, {
			method,
			headers:
				body === undefined
					? { accept: 'application/json' }
					: { accept: 'application/json', 'content-type': 'application/json' },
			body: body === undefined ? undefined : JSON.stringify(body)
		});
	} catch (err) {
		console.error(`flags: cannot reach the API for ${method} ${path}:`, err);
		return { ok: false, status: 503, errors: { fields: {}, form: 'The server is not available. Try again later.' } };
	}
	if (res.ok) {
		if (res.status === 204) return { ok: true, data: undefined as T };
		return { ok: true, data: (await res.json()) as T };
	}
	const status = res.status >= 400 && res.status <= 599 ? res.status : 502;
	if (status >= 500 && status !== 503) {
		console.error(`flags: ${method} ${path} returned ${status}`);
		return { ok: false, status, errors: { fields: {}, form: 'Something went wrong on the server. Try again later.' } };
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
		errors.form = problem.detail ?? `The request failed (${status}).`;
	}
	return { ok: false, status, errors };
}

/** listFlags reads every flag with its rules. Pass the fetch of the load or the action. */
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
