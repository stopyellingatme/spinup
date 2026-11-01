import { error, fail, redirect } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
import { requireRole } from '#lib/server/auth.js';
import { problemMessage } from './schema';
import type { Actions, PageServerLoad } from './$types';

/** The number of notes on one page of the list. */
const perPage = 20;

// pageNumber reads ?page=. A missing or invalid value is the first page.
function pageNumber(value: FormDataEntryValue | null): number {
	const n = Number(value);
	return Number.isSafeInteger(n) && n > 0 ? n : 1;
}

// id reads a note id. It returns 0 when the value is not a positive whole number.
function id(value: FormDataEntryValue | null): number {
	const n = Number(value);
	return Number.isSafeInteger(n) && n > 0 ? n : 0;
}

export const load: PageServerLoad = async ({ fetch, locals, url }) => {
	requireRole(locals, url, 'manager', 'staff');
	const page = pageNumber(url.searchParams.get('page'));
	const { data, error: problem, response } = await api.GET('/api/notes', {
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
	delete: async ({ request, fetch, locals, url }) => {
		requireRole(locals, url, 'manager', 'staff');
		const form = await request.formData();
		const path = { id: id(form.get('id')) };
		if (path.id === 0) return fail(400, { message: 'The request has no note id.' });
		const { error: problem, response } = await api.DELETE('/api/notes/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path }
		});
		// A note that is already gone is not an error.
		if (!response.ok && response.status !== 404) {
			return fail(response.status, { message: problemMessage(problem, response.status) });
		}
		const page = pageNumber(url.searchParams.get('page'));
		redirect(303, page > 1 ? `/notes?page=${page}&deleted=1` : '/notes?deleted=1');
	}
};
