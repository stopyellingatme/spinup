import { error, fail, redirect } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
import { message, parse, problemErrors, toValues } from '../form';
import type { Actions, PageServerLoad } from './$types';

// id returns the route id, or 0 when it is not a positive whole number.
function id(param: string): number {
	const n = Number(param);
	return Number.isSafeInteger(n) && n > 0 ? n : 0;
}

export const load: PageServerLoad = async ({ fetch, params }) => {
	const path = { id: id(params.id) };
	if (path.id === 0) error(404, 'This todo does not exist.');
	const { data, error: problem, response } = await api.GET('/api/todos/{id}', {
		fetch,
		baseUrl: __PUBLIC_ORIGIN__,
		params: { path }
	});
	if (response.status === 404) error(404, 'This todo does not exist.');
	if (!data) error(response.status, message(problem, response.status));
	return { item: data, values: toValues(data) };
};

export const actions: Actions = {
	update: async ({ request, fetch, params }) => {
		const { values, body, errors } = parse(await request.formData());
		if (Object.keys(errors).length > 0) {
			return fail(422, { values, errors, message: 'Correct the marked fields.' });
		}
		const { data, error: problem, response } = await api.PUT('/api/todos/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path: { id: id(params.id) } },
			body
		});
		if (!data) {
			return fail(response.status, {
				values,
				errors: problemErrors(problem),
				message: message(problem, response.status)
			});
		}
		redirect(303, '/todos/' + data.id);
	},
	delete: async ({ fetch, params }) => {
		const { error: problem, response } = await api.DELETE('/api/todos/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path: { id: id(params.id) } }
		});
		if (!response.ok) {
			return fail(response.status, { message: message(problem, response.status) });
		}
		redirect(303, '/todos');
	}
};
