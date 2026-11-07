import type { LayoutServerLoad } from './$types';

import { error } from '@sveltejs/kit';
import { getBilling } from '#lib/server/billing.js';

// Load the billing summary only for a signed-in user.
export const load: LayoutServerLoad = async ({ fetch, locals }) => {
 const result = locals.user ? await getBilling(fetch) : null;
 if (result && !result.ok) error(result.status, result.errors.form ?? Object.values(result.errors.fields).join(' '));
 return { user: locals.user, billing: result?.ok ? result.data : null };
};
