import { error, fail, redirect } from '@sveltejs/kit';
import {
	message,
	setError,
	superValidate,
	type ErrorStatus,
	type FormPathLeavesWithErrors,
	type SuperValidated
} from 'sveltekit-superforms';
import { zod4 } from 'sveltekit-superforms/adapters';
import { adminRole } from '#lib/apikeys.gen.js';
import { requireRole } from '#lib/server/auth.js';
import { adminRevokeKey, listAccounts, setPlan, type ApiKeyErrors } from '#lib/server/apikeys.js';
import { planSchema, revokeSchema } from './schema';
import type { Actions, PageServerLoad } from './$types';

// failure returns the answer of an action whose API call failed. The field errors of the problem
// show at their inputs. Any other problem shows above the form.
function failure<T extends Record<string, unknown>>(form: SuperValidated<T>, errors: ApiKeyErrors, status: number) {
	const code = (status >= 400 && status <= 599 ? status : 500) as ErrorStatus;
	const fields = Object.entries(errors.fields).filter(([field]) => field in form.data);
	if (fields.length === 0) {
		return message(form, errors.form ?? Object.values(errors.fields).join(' '), { status: code });
	}
	for (const [field, text] of fields) {
		setError(form, field as FormPathLeavesWithErrors<T>, text, { status: code });
	}
	return fail(code, { form });
}

export const load: PageServerLoad = async ({ fetch, locals, url }) => {
	requireRole(locals, url, adminRole);
	const result = await listAccounts(fetch);
	if (!result.ok) {
		error(result.status, result.errors.form ?? Object.values(result.errors.fields).join(' '));
	}
	return { admin: result.data };
};

export const actions: Actions = {
	plan: async ({ request, fetch, locals, url }) => {
		requireRole(locals, url, adminRole);
		const form = await superValidate(request, zod4(planSchema));
		if (!form.valid) return fail(400, { form });
		const result = await setPlan(fetch, form.data.email, form.data.plan);
		if (!result.ok) return failure(form, result.errors, result.status);
		redirect(303, '/admin/apikeys?saved=1');
	},
	revoke: async ({ request, fetch, locals, url }) => {
		requireRole(locals, url, adminRole);
		const form = await superValidate(request, zod4(revokeSchema));
		if (!form.valid) return fail(400, { revokeError: 'The key to revoke is not valid. Try: reload the page.' });
		const result = await adminRevokeKey(fetch, form.data.id);
		if (!result.ok) {
			const text = result.errors.form ?? Object.values(result.errors.fields).join(' ');
			return fail(result.status >= 400 && result.status <= 599 ? result.status : 500, { revokeError: text });
		}
		redirect(303, '/admin/apikeys?revoked=1');
	}
};
