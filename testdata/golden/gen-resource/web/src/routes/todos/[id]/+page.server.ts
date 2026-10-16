import { error, fail, redirect } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
import { problemMessage } from '../schema';
import type { Actions, PageServerLoad } from './$types';

const notFound = 'This todo does not exist.';

// id reads the route id. It returns 0 when the value is not a positive whole number.
function id(param: string): number {
	const n = Number(param);
	return Number.isSafeInteger(n) && n > 0 ? n : 0;
}

export const load: PageServerLoad = async ({ fetch, params }) => {
	const path = { id: id(params.id) };
	if (path.id === 0) error(404, notFound);
	const { data, error: problem, response } = await api.GET('/api/todos/{id}', {
		fetch,
		baseUrl: __PUBLIC_ORIGIN__,
		params: { path }
	});
	if (response.status === 404) error(404, notFound);
	if (!data) error(response.status, problemMessage(problem, response.status));
	return { item: data };
};

export const actions: Actions = {
	delete: async ({ fetch, params }) => {
		const path = { id: id(params.id) };
		if (path.id === 0) error(404, notFound);
		const { error: problem, response } = await api.DELETE('/api/todos/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path }
		});
		if (response.status === 404) error(404, notFound);
		if (!response.ok) return fail(response.status, { message: problemMessage(problem, response.status) });
		redirect(303, '/todos?deleted=1');
	}
};
