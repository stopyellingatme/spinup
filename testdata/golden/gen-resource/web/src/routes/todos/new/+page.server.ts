import { fail, redirect } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
import { empty, message, parse, problemErrors } from '../form';
import type { Actions, PageServerLoad } from './$types';

export const load: PageServerLoad = () => ({ values: empty });

export const actions: Actions = {
	default: async ({ request, fetch }) => {
		const { values, body, errors } = parse(await request.formData());
		if (Object.keys(errors).length > 0) {
			return fail(422, { values, errors, message: 'Correct the marked fields.' });
		}
		const { data, error: problem, response } = await api.POST('/api/todos', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
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
	}
};
