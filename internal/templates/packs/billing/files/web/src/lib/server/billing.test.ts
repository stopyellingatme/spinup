// Managed by spinup: `spinup upgrade` keeps this file current.
import type { Action } from '@sveltejs/kit';
import { describe, expect, it, vi } from 'vitest';
import { actions as pricingActions } from '../../routes/pricing/+page.server.js';
import { actions as billingActions } from '../../routes/settings/billing/+page.server.js';

// Stripe hosts the checkout page and the portal, so each URL is on a different origin from the app.
const checkoutURL = 'https://checkout.stripe.com/c/pay/cs_test_1';
const portalURL = 'https://billing.stripe.com/p/session/bps_1';

vi.mock('./billing.js', () => ({
	listPlans: vi.fn(),
	getBilling: vi.fn(),
	syncBilling: vi.fn(),
	checkout: vi.fn(async () => ({ ok: true, data: { url: checkoutURL } })),
	portal: vi.fn(async () => ({ ok: true, data: { url: portalURL } }))
}));

/** run calls a form action as a signed-in user, and returns what the action throws. */
async function run(action: Action, path: string, fields: Record<string, string>): Promise<unknown> {
	const url = new URL('https://app.example' + path);
	const request = new Request(url, { method: 'POST', body: new URLSearchParams(fields) });
	const event = { request, url, fetch, locals: { user: { id: 1, email: 'a@example.com' } } };
	try {
		return await action(event as unknown as Parameters<Action>[0]);
	} catch (thrown) {
		return thrown;
	}
}

describe('billing redirects', () => {
	// SvelteKit refuses a redirect to a different origin unless the action allows it.
	it('sends the user to the Stripe checkout page', async () => {
		const result = await run(pricingActions.checkout as Action, '/pricing?/checkout', { price: 'pro_monthly' });
		expect(result).toMatchObject({ status: 303, location: checkoutURL });
	});

	it('sends the user to the Stripe portal', async () => {
		const result = await run(billingActions.portal as Action, '/settings/billing?/portal', {});
		expect(result).toMatchObject({ status: 303, location: portalURL });
	});
});
