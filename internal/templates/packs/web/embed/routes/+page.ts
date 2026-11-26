import { redirect } from '@sveltejs/kit';
import { home } from '#lib/nav.gen.js';
import { requireVerified } from '#lib/session.svelte.js';
import type { PageLoad } from './$types';

// Signed-in users start on the first page of the app. nav.gen.ts sets home. While the last session
// read failed, requireVerified throws a 503 error: the root layout shows the retry card.
export const load: PageLoad = async ({ parent }) => {
	const { user } = await parent();
	requireVerified();
	if (user && home !== '/') redirect(303, home);
};
