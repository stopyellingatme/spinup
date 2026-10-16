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
import { api } from '#lib/api/client.js';
import { fromItem, problemMessage, schema, toBody, type Item, type Problem, type Values } from '../../schema';
import type { Actions, PageServerLoad } from './$types';

const notFound = 'This todo does not exist.';

// id reads the route id. It returns 0 when the value is not a positive whole number.
function id(param: string): number {
	const n = Number(param);
	return Number.isSafeInteger(n) && n > 0 ? n : 0;
}

// get returns a todo, or shows the 404 page when it does not exist.
async function get(fetch: typeof globalThis.fetch, param: string): Promise<Item> {
	const path = { id: id(param) };
	if (path.id === 0) error(404, notFound);
	const { data, error: problem, response } = await api.GET('/api/todos/{id}', {
		fetch,
		baseUrl: __PUBLIC_ORIGIN__,
		params: { path }
	});
	if (response.status === 404) error(404, notFound);
	if (!data) error(response.status, problemMessage(problem, response.status));
	return data;
}

// failure returns the answer of an action whose API call failed. The field errors of the problem
// show at their inputs. Any other problem shows above the form.
function failure(form: SuperValidated<Values>, problem: Problem | undefined, status: number) {
	const code = (status >= 400 && status <= 599 ? status : 500) as ErrorStatus;
	const errors = (problem?.errors ?? []).filter((e) => e.field in form.data);
	if (errors.length === 0) return message(form, problemMessage(problem, status), { status: code });
	for (const e of errors) {
		setError(form, e.field as FormPathLeavesWithErrors<Values>, e.message, { status: code });
	}
	return fail(code, { form });
}

export const load: PageServerLoad = async ({ fetch, params }) => {
	const item = await get(fetch, params.id);
	return { item, form: await superValidate(fromItem(item), zod4(schema), { errors: false }) };
};

export const actions: Actions = {
	default: async ({ request, fetch, params }) => {
		const form = await superValidate(request, zod4(schema));
		if (!form.valid) return fail(400, { form });
		const path = { id: id(params.id) };
		if (path.id === 0) error(404, notFound);
		const { data, error: problem, response } = await api.PUT('/api/todos/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path },
			body: toBody(form.data)
		});
		if (response.status === 404) error(404, notFound);
		if (!data) return failure(form, problem, response.status);
		redirect(303, `/todos/${data.id}?saved=1`);
	}
};
