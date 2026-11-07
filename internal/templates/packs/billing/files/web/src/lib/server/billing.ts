// Managed by spinup: `spinup upgrade` keeps this file current.
// The billing pages use the Go API through these helpers (ADR 0032).

/** BillingSummary describes the entitlement of the signed-in user. */
export interface BillingSummary {
	plan: string;
	features: string[];
	status: string;
	current_period_end: string | null;
	cancel_at_period_end: boolean;
	has_customer: boolean;
}

/** BillingPrice describes a Stripe price with a declared lookup key. */
export interface BillingPrice {
	lookup_key: string;
	currency: string;
	unit_amount: number;
	interval: string;
	interval_count: number;
	usage_type: string;
	product_name: string;
}

/** BillingPlan describes a plan and its available prices. */
export interface BillingPlan {
	name: string;
	features: string[];
	prices: BillingPrice[];
}

/** BillingErrors holds the field messages and the message for the form. */
export interface BillingErrors {
	fields: Record<string, string>;
	form?: string;
}

/** BillingResult holds the API answer or its status and messages. */
export type BillingResult<T> = { ok: true; data: T } | { ok: false; status: number; errors: BillingErrors };

interface Problem {
	detail?: string;
	errors?: { field?: string; message?: string }[];
}

async function call<T>(
	fetch: typeof globalThis.fetch,
	method: string,
	path: string,
	body?: unknown
): Promise<BillingResult<T>> {
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
		console.error(`billing: cannot reach the API for ${method} ${path}:`, err);
		return { ok: false, status: 503, errors: { fields: {}, form: 'The server is not available. Try: reload the page later.' } };
	}
	if (res.ok) {
		if (res.status === 204) return { ok: true, data: undefined as T };
		return { ok: true, data: (await res.json()) as T };
	}
	const status = res.status >= 400 && res.status <= 599 ? res.status : 502;
	if (status >= 500 && status !== 503) {
		console.error(`billing: ${method} ${path} returned ${status}`);
		return { ok: false, status, errors: { fields: {}, form: 'Something went wrong on the server. Try: reload the page later.' } };
	}
	let problem: Problem = {};
	try {
		problem = (await res.json()) as Problem;
	} catch {
		// Not problem details: use the status text below.
	}
	const errors: BillingErrors = { fields: {} };
	for (const e of problem.errors ?? []) {
		if (e.field && e.message && !errors.fields[e.field]) errors.fields[e.field] = e.message;
	}
	if (Object.keys(errors.fields).length === 0) {
		errors.form = problem.detail ?? `The request failed (${status}). Try: reload the page.`;
	}
	return { ok: false, status, errors };
}

function withFeatures(summary: BillingSummary): BillingSummary {
	return { ...summary, features: summary.features ?? [] };
}

/** getBilling reads the entitlement. Pass the fetch of the load or action. */
export async function getBilling(fetch: typeof globalThis.fetch): Promise<BillingResult<BillingSummary>> {
	const result = await call<BillingSummary>(fetch, 'GET', '/api/me/billing');
	return result.ok ? { ok: true, data: withFeatures(result.data) } : result;
}

/** listPlans reads the public catalog and normalizes empty lists. */
export async function listPlans(fetch: typeof globalThis.fetch): Promise<BillingResult<{ plans: BillingPlan[] }>> {
	const result = await call<{ plans: BillingPlan[] }>(fetch, 'GET', '/api/billing/plans');
	if (!result.ok) return result;
	return { ok: true, data: { plans: (result.data.plans ?? []).map((plan) => ({ ...plan, features: plan.features ?? [], prices: plan.prices ?? [] })) } };
}

/** checkout creates a hosted Checkout session for a price lookup key. */
export function checkout(fetch: typeof globalThis.fetch, price: string): Promise<BillingResult<{ url: string }>> {
	return call(fetch, 'POST', '/api/me/billing/checkout', { price });
}

/** portal creates a hosted Customer Portal session. */
export function portal(fetch: typeof globalThis.fetch): Promise<BillingResult<{ url: string }>> {
	return call(fetch, 'POST', '/api/me/billing/portal', {});
}

/** syncBilling refreshes the entitlement after Checkout returns. */
export async function syncBilling(fetch: typeof globalThis.fetch, sessionID: string): Promise<BillingResult<BillingSummary>> {
	const result = await call<BillingSummary>(fetch, 'POST', '/api/me/billing/sync', { session_id: sessionID });
	return result.ok ? { ok: true, data: withFeatures(result.data) } : result;
}
