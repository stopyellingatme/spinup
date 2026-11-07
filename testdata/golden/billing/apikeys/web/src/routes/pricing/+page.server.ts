import { error, fail, redirect } from '@sveltejs/kit';
import { message, setError, superValidate, type ErrorStatus } from 'sveltekit-superforms';
import { zod4 } from 'sveltekit-superforms/adapters';
import { z } from 'zod';
import { requireUser } from '#lib/server/auth.js';
import { checkout, listPlans } from '#lib/server/billing.js';
import type { Actions, PageServerLoad } from './$types';

const checkoutSchema = z.object({ price: z.string().regex(/^[A-Za-z0-9_.-]{1,200}$/, 'Select an available price.') });

/** Load the billing data for this page. */
export const load: PageServerLoad = async ({ fetch }) => {
	const result = await listPlans(fetch);
	if (!result.ok) error(result.status, result.errors.form ?? Object.values(result.errors.fields).join(' '));
	return { plans: result.data.plans };
};

/** Validate each form and redirect to the hosted Stripe session. */
export const actions: Actions = {
	checkout: async ({ request, fetch, locals, url }) => {
		requireUser(locals, url);
		const form = await superValidate(request, zod4(checkoutSchema));
		if (!form.valid) return fail(400, { form });
		const result = await checkout(fetch, form.data.price);
		if (!result.ok) {
			const status = result.status as ErrorStatus;
			if (result.errors.fields.price) return setError(form, 'price', result.errors.fields.price, { status });
			return message(form, result.errors.form ?? Object.values(result.errors.fields).join(' '), { status });
		}
		redirect(303, result.data.url);
	}
};
