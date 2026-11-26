import { error } from '@sveltejs/kit';
import { requireRole } from '#lib/auth.js';
import { FLAGS_ADMIN_DEP, listFlags } from '#lib/flags-admin.js';
import { adminRole } from '#lib/flags.gen.js';
import type { PageLoad } from './$types';

// The guard runs before the admin read: only the admin role of spinup.toml opens this page. The Go
// API checks the role again on every call. A save invalidates FLAGS_ADMIN_DEP to read the list again.
export const load: PageLoad = async ({ fetch, parent, url, depends }) => {
	depends(FLAGS_ADMIN_DEP);
	const { user } = await parent();
	requireRole(user, url, adminRole);
	const result = await listFlags(fetch);
	if (!result.ok) {
		error(result.status, result.errors.form ?? Object.values(result.errors.fields).join(' '));
	}
	return { admin: result.data };
};
