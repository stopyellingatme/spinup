import { redirect } from '@sveltejs/kit';
import { home } from '#lib/nav.gen.js';
import type { PageServerLoad } from './$types';

// Signed-in users start on the first page of the app. nav.gen.ts sets home.
export const load: PageServerLoad = ({ locals }) => {
	if (locals.user && home !== '/') redirect(303, home);
};
