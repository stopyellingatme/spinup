// Managed by spinup: `spinup upgrade` keeps this file current.
// Sign-out of embed mode (ADR 0039). JavaScript cannot delete the HttpOnly session cookie, so only
// the Go API can end the session. Sign-out stops the live work of this tab first, then asks the API.
// On success the document goes to /login. On failure the user stays signed in and sees why.
import { toast } from 'svelte-sonner';
import { postAuth } from '#lib/auth.js';
import { announceSessionChange, flushSessionCheck } from '#lib/session-watch.js';
import { cancelSignOut, leaveSession, startSignOut } from '#lib/session.svelte.js';

let pending: Promise<boolean> | undefined;

/**
 * signOut ends the session. A second call during a sign-out gets the same promise. It resolves
 * to false when the API did not end the session. Then the user can try again.
 */
export function signOut(): Promise<boolean> {
	pending ??= run();
	return pending;
}

async function run(): Promise<boolean> {
	// Every stop callback runs here, before the first await, so no stream outlives the session.
	startSignOut();
	const result = await postAuth(globalThis.fetch, '/api/auth/logout');
	if (result.ok) {
		announceSessionChange();
		// The pause stays active: the document goes away, and no old guard can redirect it.
		leaveSession('/login');
		return true;
	}
	console.warn(`logout: the API answered ${result.status}`);
	cancelSignOut();
	pending = undefined;
	toast.error(`Sign-out failed. ${result.errors.form ?? 'Try again.'}`);
	// Deliver a change that another tab announced during the attempt.
	flushSessionCheck();
	return false;
}
