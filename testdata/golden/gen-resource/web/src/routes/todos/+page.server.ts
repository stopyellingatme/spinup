import { error } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
import { message } from './form';
import type { PageServerLoad } from './$types';

// Server-side API calls go to PUBLIC_ORIGIN. handleFetch sends them to the Go API with the cookie.
export const load: PageServerLoad = async ({ fetch }) => {
	const { data, error: problem, response } = await api.GET('/api/todos', {
		fetch,
		baseUrl: __PUBLIC_ORIGIN__,
		params: { query: { limit: 50 } }
	});
	if (!data) error(response.status, message(problem, response.status));
	return { items: data.items, total: data.total };
};
