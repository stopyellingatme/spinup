// The embed app is a single-page app: the Go binary serves index.html for every page path.
import { isHttpError } from '@sveltejs/kit';
import { FLAGS_DEP, SESSION_DEP } from '#lib/auth.js';
import { readSession, session } from '#lib/session.svelte.js';
// The session watch opens its channel when this module loads, before the first session read.
import '#lib/session-watch.js';
import type { LayoutLoad } from './$types';

export const ssr = false;

// The root load reads the session and the public flags from the Go API. It does not read url, so a
// navigation does not run it again. The session watch invalidates SESSION_DEP. A saved flag
// invalidates FLAGS_DEP. A failed read keeps the last verified data and sets sessionError: the
// layout then hides the pages and offers a retry. A thrown error here would leave a blank page.
export const load: LayoutLoad = async ({ fetch, depends }) => {
	depends(SESSION_DEP, FLAGS_DEP);
	try {
		return { ...(await readSession(fetch)), sessionError: null };
	} catch (err) {
		const message = isHttpError(err) ? err.body.message : 'The session cannot be verified. Try again later.';
		return { ...session.data, sessionError: message };
	}
};
