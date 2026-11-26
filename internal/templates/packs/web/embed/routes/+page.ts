import { redirect } from '@sveltejs/kit';
import { home } from '#lib/nav.gen.js';
import type { PageLoad } from './$types';

// Signed-in users start on the first page of the app. nav.gen.ts sets home.
export const load: PageLoad = async ({ parent }) => {
	const { user } = await parent();
	if (user && home !== '/') redirect(303, home);
};
