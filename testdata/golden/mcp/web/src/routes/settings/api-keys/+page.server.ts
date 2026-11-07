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
import { requireUser } from '#lib/server/auth.js';
import { createKey, listKeys, revokeKey, type ApiKeyErrors } from '#lib/server/apikeys.js';
import { createSchema, revokeSchema } from './schema';
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
	requireUser(locals, url);
	const result = await listKeys(fetch);
	if (!result.ok) {
		error(result.status, result.errors.form ?? Object.values(result.errors.fields).join(' '));
	}
	return { overview: result.data };
};

export const actions: Actions = {
	create: async ({ request, fetch, locals, url }) => {
		requireUser(locals, url);
		const form = await superValidate(request, zod4(createSchema));
		if (!form.valid) return fail(400, { form });
		const result = await createKey(fetch, form.data);
		if (!result.ok) return failure(form, result.errors, result.status);
		// The page shows the token once. The answer holds it, and a redirect would lose it.
		return { created: { name: result.data.key.name, display: result.data.key.display, token: result.data.token } };
	},
	revoke: async ({ request, fetch, locals, url }) => {
		requireUser(locals, url);
		const form = await superValidate(request, zod4(revokeSchema));
		if (!form.valid) return fail(400, { revokeError: 'The key to revoke is not valid. Try: reload the page.' });
		const result = await revokeKey(fetch, form.data.id);
		if (!result.ok) {
			const text = result.errors.form ?? Object.values(result.errors.fields).join(' ');
			return fail(result.status >= 400 && result.status <= 599 ? result.status : 500, { revokeError: text });
		}
		redirect(303, '/settings/api-keys?revoked=1');
	}
};
