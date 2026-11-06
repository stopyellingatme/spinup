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
import { adminRole } from '#lib/flags.gen.js';
import { requireRole } from '#lib/server/auth.js';
import { clearUser, listFlags, putRule, putUser, type FlagErrors } from '#lib/server/flags.js';
import { clearSchema, ruleSchema, userSchema } from './schema';
import type { Actions, PageServerLoad } from './$types';

// failure returns the answer of an action whose API call failed. The field errors of the problem
// show at their inputs. Any other problem shows above the row.
function failure<T extends Record<string, unknown>>(form: SuperValidated<T>, errors: FlagErrors, status: number) {
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
	const result = await listFlags(fetch);
	if (!result.ok) {
		error(result.status, result.errors.form ?? Object.values(result.errors.fields).join(' '));
	}
	return { admin: result.data };
};

export const actions: Actions = {
	rule: async ({ request, fetch, locals, url }) => {
		requireRole(locals, url, adminRole);
		const form = await superValidate(request, zod4(ruleSchema));
		if (!form.valid) return fail(400, { form });
		const { key, ...rule } = form.data;
		const result = await putRule(fetch, key, rule);
		if (!result.ok) return failure(form, result.errors, result.status);
		redirect(303, '/admin/flags?saved=1');
	},
	user: async ({ request, fetch, locals, url }) => {
		requireRole(locals, url, adminRole);
		const form = await superValidate(request, zod4(userSchema));
		if (!form.valid) return fail(400, { form });
		const result = await putUser(fetch, form.data.key, form.data.email, form.data.enabled);
		if (!result.ok) return failure(form, result.errors, result.status);
		redirect(303, '/admin/flags?saved=1');
	},
	clearUser: async ({ request, fetch, locals, url }) => {
		requireRole(locals, url, adminRole);
		const form = await superValidate(request, zod4(clearSchema));
		if (!form.valid) return fail(400, { form });
		const result = await clearUser(fetch, form.data.key, form.data.email);
		if (!result.ok) return failure(form, result.errors, result.status);
		redirect(303, '/admin/flags?saved=1');
	}
};
