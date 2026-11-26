// Managed by spinup: `spinup upgrade` keeps this file current.
// The session state of embed mode (ADR 0039): the verified user, the public flags, the sign-out
// pause, the identity change, the generation that rejects late results, and the verification
// state of the last read. This module is the one owner of that state. It keeps it in memory only:
// no storage, no cookie access.
import { error, isHttpError } from '@sveltejs/kit';
import { loadFlags, loadSession, safeRedirect } from '#lib/auth.js';
import { home } from '#lib/nav.gen.js';

/** The data of the root load: the verified user and the public flags. */
export type SessionData = { user: App.User | null; flags: Record<string, boolean> };

let data = $state.raw<SessionData>({ user: null, flags: {} });
let signingOut = $state(false);
let changingUser = $state(false);
let generation = $state(0);
// The message of the last failed session read, or null when the last read verified the session.
let unverified = $state<string | null>(null);

const notVerified = 'The session cannot be verified. Try again later.';

/** The read-only session state. Pages read it. Only the functions of this module change it. */
export const session = {
	get data(): SessionData {
		return data;
	},
	/** True from the start of a sign-out until it fails. A successful sign-out leaves the document. */
	get signingOut(): boolean {
		return signingOut;
	},
	/** True while the document goes away for another identity. Hide the shell and the page. */
	get changingUser(): boolean {
		return changingUser;
	},
	/** The generation advances on each sign-out and departure. Compare it before using a late result. */
	get generation(): number {
		return generation;
	},
	/**
	 * The message of the last failed session read, or null. The first read counts as not verified
	 * until it answers. A 401 is a verified answer: the visitor is signed out.
	 */
	get unverified(): string | null {
		return unverified;
	}
};

/**
 * requireVerified throws a 503 error with the message of the last failed session read. A page
 * guard calls it first: it must not redirect or return on data that the API did not verify. The
 * root layout hides the error page and offers a retry, and the address bar keeps the deep link.
 */
export function requireVerified(): void {
	if (unverified !== null) error(503, unverified);
}

type Pause = { stop: () => void; resume: () => void; stopped: boolean };

const pauses = new Set<Pause>();
// A promise that never settles. A load that awaits it ends with the departing document.
const departed = new Promise<never>(() => {});
let accepted = false;
let leaving = false;
let reads = 0;
let latest: Promise<SessionData> = departed;
let pauseEnd: { promise: Promise<void>; resolve: () => void } | undefined;

/**
 * readSession reads the session and the public flags at the same time. Only a 401 of the session
 * read means signed out. Any other failure rejects and keeps the last accepted identity. When a
 * newer read starts, this read returns the result of the newer read. During a sign-out it waits
 * for the outcome. During a departure it never settles, so no guard gets old data.
 */
export function readSession(fetch: typeof globalThis.fetch): Promise<SessionData> {
	if (!accepted && unverified === null) unverified = notVerified;
	const read = settle(fetch, ++reads);
	latest = read;
	return read;
}

async function settle(fetch: typeof globalThis.fetch, n: number): Promise<SessionData> {
	for (;;) {
		while (pauseEnd) await pauseEnd.promise;
		if (leaving) return departed;
		const started = generation;
		// Start both requests before the first await.
		const reading = loadSession(fetch);
		// loadFlags never rejects: a failed flags read gives an empty map.
		const flags = loadFlags(fetch);
		let user: App.User | null | undefined;
		let failure: unknown;
		try {
			user = await reading;
		} catch (err) {
			failure = err;
		}
		let next = interrupted(n, started);
		if (next === 'again') continue;
		if (next) return next;
		if (user === undefined) {
			unverified = isHttpError(failure) ? failure.body.message : notVerified;
			throw failure;
		}
		// Compare the identity before the flags answer: a stalled flags read must not keep the
		// page of one user visible after the session names another.
		if (changed(user)) return departed;
		const published = { user, flags: await flags };
		next = interrupted(n, started);
		if (next === 'again') continue;
		if (next) return next;
		return publish(published);
	}
}

// interrupted reports what a read does after an await. A departure ends it with a promise that
// never settles. A newer read gives it the result of the newer read. A sign-out that started
// during the read makes it read again: its answer belongs to the old generation.
function interrupted(n: number, started: number): Promise<SessionData> | 'again' | undefined {
	if (leaving) return departed;
	if (n !== reads) return latest;
	if (started !== generation || signingOut) return 'again';
	return undefined;
}

// changed leaves the session when the verified identity differs from the accepted one. Another
// tab, or the API, changed the identity. Never show one user's data to another.
function changed(user: App.User | null): boolean {
	if (!accepted || sameUser(data.user, user)) return false;
	leaveSession(user ? home : '/login');
	return true;
}

function publish(next: SessionData): Promise<SessionData> | SessionData {
	if (changed(next.user)) return departed;
	accepted = true;
	unverified = null;
	data = next;
	return next;
}

function sameUser(a: App.User | null, b: App.User | null): boolean {
	if (a === null || b === null) return a === b;
	return a.id === b.id && a.role === b.role && a.email === b.email;
}

/**
 * startSignOut pauses the session: it advances the generation and calls every stop callback
 * before it returns. Session reads wait until cancelSignOut or leaveSession.
 */
export function startSignOut(): void {
	if (signingOut || leaving) return;
	signingOut = true;
	generation++;
	let resolve = () => {};
	const promise = new Promise<void>((done) => (resolve = done));
	pauseEnd = { promise, resolve };
	stopAll();
}

/** cancelSignOut ends a failed sign-out: the user stays signed in and each paused callback resumes. */
export function cancelSignOut(): void {
	if (!signingOut || leaving) return;
	signingOut = false;
	const end = pauseEnd;
	pauseEnd = undefined;
	end?.resolve();
	for (const pause of pauses) {
		if (!pause.stopped) continue;
		pause.stopped = false;
		run(pause.resume, 'resume');
	}
}

/**
 * leaveSession clears the session state, stops each paused callback for good, and replaces the
 * document with target. A full document load drops every page, draft, and pending callback.
 */
export function leaveSession(target: string): void {
	if (leaving) return;
	leaving = true;
	changingUser = true;
	generation++;
	stopAll();
	data = { user: null, flags: {} };
	window.location.replace(safeRedirect(target, home));
}

/**
 * onSessionPause registers the stop and resume callbacks of a live subscription. A sign-out calls
 * stop. A failed sign-out calls resume. During a pause, registration calls stop at once. Call the
 * returned function to remove the callbacks.
 */
export function onSessionPause(stop: () => void, resume: () => void): () => void {
	const pause: Pause = { stop, resume, stopped: false };
	pauses.add(pause);
	if (signingOut || leaving) {
		pause.stopped = true;
		run(stop, 'stop');
	}
	return () => {
		pauses.delete(pause);
	};
}

function stopAll(): void {
	for (const pause of pauses) {
		if (pause.stopped) continue;
		pause.stopped = true;
		run(pause.stop, 'stop');
	}
}

// run calls one callback. A callback that throws must not keep the other callbacks from running.
function run(callback: () => void, name: string): void {
	try {
		callback();
	} catch (err) {
		console.error(`session: a ${name} callback failed:`, err);
	}
}
