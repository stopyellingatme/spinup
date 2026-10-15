import { fail } from '@sveltejs/kit';
import { postAuth } from '#lib/server/auth.js';
import type { Actions } from './$types';

export const actions: Actions = {
 default: async ({ request, fetch, cookies }) => {
  const data = await request.formData();
  const email = String(data.get('email') ?? '');
  const result = await postAuth(fetch, cookies, '/api/auth/reset/request', { email });
  if (!result.ok) return fail(result.status, { errors: result.errors, sent: false });
  return { errors: { form: undefined }, sent: true };
 }
};
