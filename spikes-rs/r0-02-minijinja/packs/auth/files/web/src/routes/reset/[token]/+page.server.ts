import { fail, redirect } from '@sveltejs/kit';
import { postAuth } from '#lib/server/auth.js';
import type { Actions, PageServerLoad } from './$types';

export const load: PageServerLoad = ({ setHeaders }) => {
 setHeaders({ 'referrer-policy': 'no-referrer', 'cache-control': 'no-store' });
 return {};
};

export const actions: Actions = {
 default: async ({ request, fetch, cookies, params }) => {
  const data = await request.formData();
  const password = String(data.get('password') ?? '');
  const result = await postAuth(fetch, cookies, '/api/auth/reset/confirm', { token: params.token, password });
  if (!result.ok) return fail(result.status, { errors: result.errors });
  redirect(303, '/login');
 }
};
