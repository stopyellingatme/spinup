import { error, fail, redirect } from '@sveltejs/kit';
import { message, superValidate, type ErrorStatus } from 'sveltekit-superforms';
import { zod4 } from 'sveltekit-superforms/adapters';
import { z } from 'zod';
import { requireUser } from '#lib/server/auth.js';
import { getBilling, portal, syncBilling } from '#lib/server/billing.js';
import type { Actions, PageServerLoad } from './$types';

const portalSchema = z.object({});

/** Load the billing data for this page. */
export const load: PageServerLoad = async ({ fetch, locals, url }) => {
	requireUser(locals, url);
	if (url.searchParams.has('session_id')) {
		const result = await syncBilling(fetch, url.searchParams.get('session_id') ?? '');
		if (!result.ok) error(result.status, result.errors.form ?? Object.values(result.errors.fields).join(' '));
		const clean = new URL(url);
		clean.searchParams.delete('session_id');
		redirect(303, clean.pathname + clean.search + clean.hash);
	}
	const result = await getBilling(fetch);
	if (!result.ok) error(result.status, result.errors.form ?? Object.values(result.errors.fields).join(' '));
	return { billing: result.data };
};

/** Validate each form and redirect to the hosted Stripe session. */
export const actions: Actions = {
	portal: async ({ request, fetch, locals, url }) => {
		requireUser(locals, url);
		const form = await superValidate(request, zod4(portalSchema));
		if (!form.valid) return fail(400, { form });
		const result = await portal(fetch);
		if (!result.ok) return message(form, result.errors.form ?? Object.values(result.errors.fields).join(' '), { status: result.status as ErrorStatus });
		redirect(303, result.data.url);
	}
};
