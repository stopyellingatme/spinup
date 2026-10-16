import { error, fail, redirect } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
import { fromItem, problemMessage, toBody } from './schema';
import type { Actions, PageServerLoad } from './$types';

/** The number of todos on one page of the list. */
const perPage = 20;

// pageNumber reads ?page=. A missing or invalid value is the first page.
function pageNumber(value: FormDataEntryValue | null): number {
	const n = Number(value);
	return Number.isSafeInteger(n) && n > 0 ? n : 1;
}

// id reads a todo id. It returns 0 when the value is not a positive whole number.
function id(value: FormDataEntryValue | null): number {
	const n = Number(value);
	return Number.isSafeInteger(n) && n > 0 ? n : 0;
}

export const load: PageServerLoad = async ({ fetch, url }) => {
	const page = pageNumber(url.searchParams.get('page'));
	const { data, error: problem, response } = await api.GET('/api/todos', {
		fetch,
		baseUrl: __PUBLIC_ORIGIN__,
		params: { query: { limit: perPage, offset: (page - 1) * perPage } }
	});
	if (!data) error(response.status, problemMessage(problem, response.status));
	const pages = Math.max(1, Math.ceil(data.total / perPage));
	if (page > pages) {
		// The page is past the end, for example after a delete: show the last page.
		const last = new URL(url);
		last.searchParams.set('page', String(pages));
		redirect(303, last.pathname + last.search);
	}
	return { items: data.items, total: data.total, page, perPage };
};

export const actions: Actions = {
	// toggle sets done of one todo. PUT replaces the whole todo, so it sends every field.
	toggle: async ({ request, fetch }) => {
		const form = await request.formData();
		const path = { id: id(form.get('id')) };
		if (path.id === 0) return fail(400, { message: 'The request has no todo id.' });
		const { data: item, error: problem, response } = await api.GET('/api/todos/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path }
		});
		if (!item) return fail(response.status, { message: problemMessage(problem, response.status) });
		const saved = await api.PUT('/api/todos/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path },
			body: { ...toBody(fromItem(item)), done: form.get('value') === 'true' }
		});
		if (!saved.data) {
			return fail(saved.response.status, { message: problemMessage(saved.error, saved.response.status) });
		}
		return { saved: path.id };
	},
	delete: async ({ request, fetch, url }) => {
		const form = await request.formData();
		const path = { id: id(form.get('id')) };
		if (path.id === 0) return fail(400, { message: 'The request has no todo id.' });
		const { error: problem, response } = await api.DELETE('/api/todos/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path }
		});
		// A todo that is already gone is not an error.
		if (!response.ok && response.status !== 404) {
			return fail(response.status, { message: problemMessage(problem, response.status) });
		}
		const page = pageNumber(url.searchParams.get('page'));
		redirect(303, page > 1 ? `/todos?page=${page}&deleted=1` : '/todos?deleted=1');
	}
};
