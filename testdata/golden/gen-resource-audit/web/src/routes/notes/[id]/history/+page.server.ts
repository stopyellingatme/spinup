import { error } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
import { requireRole } from '#lib/server/auth.js';
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ fetch, locals, url, params, setHeaders }) => {
	requireRole(locals, url, 'manager');
	setHeaders({ 'cache-control': 'no-store' });
	const id = Number(params.id);
	if (!Number.isSafeInteger(id) || id < 1) error(400, 'Invalid record ID. Try: use a positive whole number.');
	const cursor = url.searchParams.get('before_id');
	const before = cursor === null ? undefined : Number(cursor);
	if (before !== undefined && (!Number.isSafeInteger(before) || before < 1)) {
		error(422, 'Invalid history cursor. Try: open the newest events.');
	}
	const { data, response } = await api.GET('/api/notes/{id}/history', {
		fetch,
		baseUrl: __PUBLIC_ORIGIN__,
		params: { path: { id }, query: { limit: 50, before_id: before } }
	});
	if (!data) error(response.status, 'Cannot read history. Try: retry the request.');
	const events = data.items;
	return { id, events, nextBeforeID: events.length === 50 ? events[events.length - 1].id : null };
};
