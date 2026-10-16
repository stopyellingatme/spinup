package resource

// The server files of the pages. Every load and action of an app with auth
// starts with requireUser (ADR 0015 §5). Server-side API calls go to
// PUBLIC_ORIGIN, and handleFetch sends them to the Go API with the cookie.

const listServerTS = `import { error, fail, redirect } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
[[- if .Auth ]]
import { requireUser } from '#lib/server/auth.js';
[[- end ]]
import { [[ if .Toggle ]]fromItem, [[ end ]]problemMessage[[ if .Toggle ]], toBody[[ end ]] } from './schema';
import type { Actions, PageServerLoad } from './$types';

/** The number of [[ .PluralWords ]] on one page of the list. */
const perPage = 20;

// pageNumber reads ?page=. A missing or invalid value is the first page.
function pageNumber(value: FormDataEntryValue | null): number {
	const n = Number(value);
	return Number.isSafeInteger(n) && n > 0 ? n : 1;
}

// id reads a [[ .Words ]] id. It returns 0 when the value is not a positive whole number.
function id(value: FormDataEntryValue | null): number {
	const n = Number(value);
	return Number.isSafeInteger(n) && n > 0 ? n : 0;
}

export const load: PageServerLoad = async ({ fetch, [[ if .Auth ]]locals, [[ end ]]url }) => {
[[- if .Auth ]]
	requireUser(locals, url);
[[- end ]]
	const page = pageNumber(url.searchParams.get('page'));
	const { data, error: problem, response } = await api.GET('[[ .APIPath ]]', {
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
[[- if .Toggle ]]
	// toggle sets [[ .Toggle.Col ]] of one [[ .Words ]]. PUT replaces the whole [[ .Words ]], so it sends every field.
	toggle: async ({ request, fetch[[ if .Auth ]], locals, url[[ end ]] }) => {
[[- if .Auth ]]
		requireUser(locals, url);
[[- end ]]
		const form = await request.formData();
		const path = { id: id(form.get('id')) };
		if (path.id === 0) return fail(400, { message: 'The request has no [[ .Words ]] id.' });
		const { data: item, error: problem, response } = await api.GET('[[ .APIPath ]]/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path }
		});
		if (!item) return fail(response.status, { message: problemMessage(problem, response.status) });
		const saved = await api.PUT('[[ .APIPath ]]/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path },
			body: { ...toBody(fromItem(item)[[ if .Time ]], item[[ end ]]), [[ .Toggle.Col ]]: form.get('value') === 'true' }
		});
		if (!saved.data) {
			return fail(saved.response.status, { message: problemMessage(saved.error, saved.response.status) });
		}
		return { saved: path.id };
	},
[[- end ]]
	delete: async ({ request, fetch[[ if .Auth ]], locals[[ end ]], url }) => {
[[- if .Auth ]]
		requireUser(locals, url);
[[- end ]]
		const form = await request.formData();
		const path = { id: id(form.get('id')) };
		if (path.id === 0) return fail(400, { message: 'The request has no [[ .Words ]] id.' });
		const { error: problem, response } = await api.DELETE('[[ .APIPath ]]/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path }
		});
		// A [[ .Words ]] that is already gone is not an error.
		if (!response.ok && response.status !== 404) {
			return fail(response.status, { message: problemMessage(problem, response.status) });
		}
		const page = pageNumber(url.searchParams.get('page'));
		redirect(303, page > 1 ? ` + "`" + `[[ .Route ]]?page=${page}&deleted=1` + "`" + ` : '[[ .Route ]]?deleted=1');
	}
};
`

const itemServerTS = `import { error, fail, redirect } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
[[- if .Auth ]]
import { requireUser } from '#lib/server/auth.js';
[[- end ]]
import { problemMessage } from '../schema';
import type { Actions, PageServerLoad } from './$types';

const notFound = 'This [[ .Words ]] does not exist.';

// id reads the route id. It returns 0 when the value is not a positive whole number.
function id(param: string): number {
	const n = Number(param);
	return Number.isSafeInteger(n) && n > 0 ? n : 0;
}

export const load: PageServerLoad = async ({ fetch, params[[ if .Auth ]], locals, url[[ end ]] }) => {
[[- if .Auth ]]
	requireUser(locals, url);
[[- end ]]
	const path = { id: id(params.id) };
	if (path.id === 0) error(404, notFound);
	const { data, error: problem, response } = await api.GET('[[ .APIPath ]]/{id}', {
		fetch,
		baseUrl: __PUBLIC_ORIGIN__,
		params: { path }
	});
	if (response.status === 404) error(404, notFound);
	if (!data) error(response.status, problemMessage(problem, response.status));
	return { item: data };
};

export const actions: Actions = {
	delete: async ({ fetch, params[[ if .Auth ]], locals, url[[ end ]] }) => {
[[- if .Auth ]]
		requireUser(locals, url);
[[- end ]]
		const path = { id: id(params.id) };
		if (path.id === 0) error(404, notFound);
		const { error: problem, response } = await api.DELETE('[[ .APIPath ]]/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path }
		});
		if (response.status === 404) error(404, notFound);
		if (!response.ok) return fail(response.status, { message: problemMessage(problem, response.status) });
		redirect(303, '[[ .Route ]]?deleted=1');
	}
};
`

const editServerTS = `import { error, fail, redirect } from '@sveltejs/kit';
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
[[- if .Auth ]]
import { requireUser } from '#lib/server/auth.js';
[[- end ]]
import { fromItem, problemMessage, schema, toBody, type Item, type Problem, type Values } from '../../schema';
import type { Actions, PageServerLoad } from './$types';

const notFound = 'This [[ .Words ]] does not exist.';

// id reads the route id. It returns 0 when the value is not a positive whole number.
function id(param: string): number {
	const n = Number(param);
	return Number.isSafeInteger(n) && n > 0 ? n : 0;
}

// get returns a [[ .Words ]], or shows the 404 page when it does not exist.
async function get(fetch: typeof globalThis.fetch, param: string): Promise<Item> {
	const path = { id: id(param) };
	if (path.id === 0) error(404, notFound);
	const { data, error: problem, response } = await api.GET('[[ .APIPath ]]/{id}', {
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

export const load: PageServerLoad = async ({ fetch, params[[ if .Auth ]], locals, url[[ end ]] }) => {
[[- if .Auth ]]
	requireUser(locals, url);
[[- end ]]
	const item = await get(fetch, params.id);
	return { item, form: await superValidate(fromItem(item), zod4(schema), { errors: false }) };
};

export const actions: Actions = {
	default: async ({ request, fetch, params[[ if .Auth ]], locals, url[[ end ]] }) => {
[[- if .Auth ]]
		requireUser(locals, url);
[[- end ]]
		const form = await superValidate(request, zod4(schema));
		if (!form.valid) return fail(400, { form });
[[- if .Time ]]
		const original = await get(fetch, params.id);
		const { data, error: problem, response } = await api.PUT('[[ .APIPath ]]/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path: { id: original.id } },
			body: toBody(form.data, original)
		});
[[- else ]]
		const path = { id: id(params.id) };
		if (path.id === 0) error(404, notFound);
		const { data, error: problem, response } = await api.PUT('[[ .APIPath ]]/{id}', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			params: { path },
			body: toBody(form.data)
		});
[[- end ]]
		if (response.status === 404) error(404, notFound);
		if (!data) return failure(form, problem, response.status);
		redirect(303, ` + "`" + `[[ .Route ]]/${data.id}?saved=1` + "`" + `);
	}
};
`

const newServerTS = `import { fail, redirect } from '@sveltejs/kit';
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
[[- if .Auth ]]
import { requireUser } from '#lib/server/auth.js';
[[- end ]]
import { problemMessage, schema, toBody, type Problem, type Values } from '../schema';
import type { Actions, PageServerLoad } from './$types';

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

export const load: PageServerLoad = async ([[ if .Auth ]]{ locals, url }[[ end ]]) => {
[[- if .Auth ]]
	requireUser(locals, url);
[[- end ]]
	return { form: await superValidate(zod4(schema)) };
};

export const actions: Actions = {
	default: async ({ request, fetch[[ if .Auth ]], locals, url[[ end ]] }) => {
[[- if .Auth ]]
		requireUser(locals, url);
[[- end ]]
		const form = await superValidate(request, zod4(schema));
		if (!form.valid) return fail(400, { form });
		const { data, error: problem, response } = await api.POST('[[ .APIPath ]]', {
			fetch,
			baseUrl: __PUBLIC_ORIGIN__,
			body: toBody(form.data)
		});
		if (!data) return failure(form, problem, response.status);
		redirect(303, ` + "`" + `[[ .Route ]]/${data.id}?created=1` + "`" + `);
	}
};
`
