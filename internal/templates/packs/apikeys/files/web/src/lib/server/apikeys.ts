// Managed by spinup: `spinup upgrade` keeps this file current.
// The key pages call the key endpoints of the Go API with these helpers (ADR 0030).

/** The scope of a key: read allows GET and HEAD, write allows all methods. */
export type KeyScope = 'read' | 'write';

/** ApiKey is one key without its secret. Dates are RFC 3339 strings. */
export interface ApiKey {
	id: number;
	name: string;
	scope: KeyScope;
	display: string;
	created_at: string;
	last_used_at: string | null;
	expires_at: string | null;
}

/** Plan is a named plan with its limits. */
export interface Plan {
	name: string;
	per_minute: number;
	per_day: number;
}

/** UsageDay is the request count of one key on one UTC day (YYYY-MM-DD). */
export interface UsageDay {
	key_id: number;
	day: string;
	count: number;
}

/** KeysOverview is the answer of GET /api/me/keys. */
export interface KeysOverview {
	plan: Plan;
	max_keys: number;
	today: number;
	keys: ApiKey[];
	usage: UsageDay[];
}

/** NewKey is the answer of POST /api/me/keys. The token is shown once. */
export interface NewKey {
	key: ApiKey;
	token: string;
}

/** CreateKeyInput is the body of a key create. Leave expires_in_days out for a key that does not expire. */
export interface CreateKeyInput {
	name: string;
	scope: KeyScope;
	expires_in_days?: number;
}

/** Account is one user with a key or a plan row. */
export interface Account {
	user_id: number;
	email: string;
	role: string;
	plan: string;
	today: number;
	keys: ApiKey[];
}

/** AccountsAdmin is the answer of GET /api/admin/apikeys. */
export interface AccountsAdmin {
	default_plan: string;
	plans: Plan[];
	accounts: Account[];
}

/** ApiKeyErrors holds the messages for the fields of a key form, and for the form as a whole. */
export interface ApiKeyErrors {
	fields: Record<string, string>;
	form?: string;
}

/** ApiKeyResult is ok with the data, or an HTTP status for fail() and the messages to show. */
export type ApiKeyResult<T> = { ok: true; data: T } | { ok: false; status: number; errors: ApiKeyErrors };

interface Problem {
	detail?: string;
	errors?: { field?: string; message?: string }[];
}

async function call<T>(
	fetch: typeof globalThis.fetch,
	method: string,
	path: string,
	body?: unknown
): Promise<ApiKeyResult<T>> {
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
		console.error(`apikeys: cannot reach the API for ${method} ${path}:`, err);
		return { ok: false, status: 503, errors: { fields: {}, form: 'The server is not available. Try again later.' } };
	}
	if (res.ok) {
		if (res.status === 204) return { ok: true, data: undefined as T };
		return { ok: true, data: (await res.json()) as T };
	}
	const status = res.status >= 400 && res.status <= 599 ? res.status : 502;
	if (status >= 500 && status !== 503) {
		console.error(`apikeys: ${method} ${path} returned ${status}`);
		return { ok: false, status, errors: { fields: {}, form: 'Something went wrong on the server. Try again later.' } };
	}
	let problem: Problem = {};
	try {
		problem = (await res.json()) as Problem;
	} catch {
		// Not problem details: use the status text below.
	}
	const errors: ApiKeyErrors = { fields: {} };
	for (const e of problem.errors ?? []) {
		if (e.field && e.message && !errors.fields[e.field]) errors.fields[e.field] = e.message;
	}
	if (Object.keys(errors.fields).length === 0) {
		errors.form = problem.detail ?? `The request failed (${status}).`;
	}
	return { ok: false, status, errors };
}

// The API can answer null for an empty list. The pages loop over these lists.
function withLists<T extends { keys?: ApiKey[] | null }>(value: T): T & { keys: ApiKey[] } {
	return { ...value, keys: value.keys ?? [] };
}

/** listKeys reads the plan, the keys, and the 30-day usage of the signed-in user. Pass the fetch of the load or the action. */
export async function listKeys(fetch: typeof globalThis.fetch): Promise<ApiKeyResult<KeysOverview>> {
	const result = await call<KeysOverview>(fetch, 'GET', '/api/me/keys');
	if (!result.ok) return result;
	return { ok: true, data: { ...withLists(result.data), usage: result.data.usage ?? [] } };
}

/** createKey makes a key for the signed-in user. The token in the answer is the only time the secret shows. */
export function createKey(fetch: typeof globalThis.fetch, input: CreateKeyInput): Promise<ApiKeyResult<NewKey>> {
	return call<NewKey>(fetch, 'POST', '/api/me/keys', input);
}

/** revokeKey revokes a key of the signed-in user. A key of another user is a 404. */
export function revokeKey(fetch: typeof globalThis.fetch, id: number): Promise<ApiKeyResult<void>> {
	return call<void>(fetch, 'DELETE', `/api/me/keys/${id}`);
}

/** listAccounts reads the plans and the accounts for the admin page. */
export async function listAccounts(fetch: typeof globalThis.fetch): Promise<ApiKeyResult<AccountsAdmin>> {
	const result = await call<AccountsAdmin>(fetch, 'GET', '/api/admin/apikeys');
	if (!result.ok) return result;
	return {
		ok: true,
		data: {
			...result.data,
			plans: result.data.plans ?? [],
			accounts: (result.data.accounts ?? []).map(withLists)
		}
	};
}

/** setPlan sets the plan of the account with this email address. */
export async function setPlan(fetch: typeof globalThis.fetch, email: string, plan: string): Promise<ApiKeyResult<Account>> {
	const result = await call<Account>(fetch, 'PUT', `/api/admin/apikeys/users/${encodeURIComponent(email)}/plan`, { plan });
	if (!result.ok) return result;
	return { ok: true, data: withLists(result.data) };
}

/** adminRevokeKey revokes a key of any user. */
export function adminRevokeKey(fetch: typeof globalThis.fetch, id: number): Promise<ApiKeyResult<void>> {
	return call<void>(fetch, 'DELETE', `/api/admin/apikeys/keys/${id}`);
}
