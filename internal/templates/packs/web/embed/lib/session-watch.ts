// Managed by spinup: `spinup upgrade` keeps this file current.
// The session watch of embed mode (ADR 0039). The root load reads the session once, so this module
// asks for a new read when another tab announces a change, when the tab becomes visible, when the
// back-forward cache restores the page, and when an API call answers 401. Requests coalesce into one
// read. No read starts during a navigation, a sign-out, or a departure.
import { invalidate } from '$app/navigation';
import { navigating } from '$app/state';
import { SESSION_DEP } from '#lib/auth.js';
import { session } from '#lib/session.svelte.js';

const CHANNEL = 'spinup:session';
const OIDC_PENDING = 'spinup:oidc-pending';

// The channel opens when the root imports this module, before the first session read. Messages
// that arrive before watchSession wait in `wanted`.
let channel = openChannel();
let mounted = false;
let wanted = false;
let checking = false;

function openChannel(): BroadcastChannel | null {
	if (typeof BroadcastChannel === 'undefined') return null;
	const opened = new BroadcastChannel(CHANNEL);
	opened.onmessage = (event: MessageEvent) => {
		if (isChange(event.data)) recheckSession();
	};
	return opened;
}

// isChange accepts only {type:'session-changed'}. A message never carries an identity.
function isChange(message: unknown): boolean {
	if (typeof message !== 'object' || message === null) return false;
	const keys = Object.keys(message);
	return keys.length === 1 && (message as { type?: unknown }).type === 'session-changed';
}

/**
 * watchSession starts the watch. Call it once from the root layout, after the first session read.
 * It handles the buffered requests and returns the teardown, which removes the listeners and closes
 * the channel.
 */
export function watchSession(): () => void {
	if (mounted) return () => {};
	mounted = true;
	channel ??= openChannel();
	consumeOidcReturn();
	const onVisible = () => {
		if (document.visibilityState === 'visible') recheckSession();
	};
	const onPageShow = (event: PageTransitionEvent) => {
		if (event.persisted) recheckSession();
	};
	document.addEventListener('visibilitychange', onVisible);
	window.addEventListener('pageshow', onPageShow);
	flushSessionCheck();
	return () => {
		mounted = false;
		document.removeEventListener('visibilitychange', onVisible);
		window.removeEventListener('pageshow', onPageShow);
		channel?.close();
		channel = null;
	};
}

// consumeOidcReturn removes the marker that the sign-in page sets before an OIDC redirect. A
// return with a user is a sign-in, so the other tabs must hear about it.
function consumeOidcReturn(): void {
	let pending: boolean;
	try {
		pending = sessionStorage.getItem(OIDC_PENDING) !== null;
		if (pending) sessionStorage.removeItem(OIDC_PENDING);
	} catch {
		return;
	}
	if (pending && session.data.user) announceSessionChange();
}

/** recheckSession asks for a new read of the session and the public flags. */
export function recheckSession(): void {
	wanted = true;
	flushSessionCheck();
}

/**
 * flushSessionCheck starts the requested read when nothing blocks it. During a navigation an
 * invalidation does not reach the page that the navigation shows, so the root calls this function
 * again when each navigation completes. A request during a read starts one more read after it.
 */
export function flushSessionCheck(): void {
	if (!wanted || !mounted || checking) return;
	if (session.signingOut || session.changingUser || navigating.to) return;
	wanted = false;
	checking = true;
	invalidate(SESSION_DEP)
		.catch((err: unknown) => console.error('session: the session check failed:', err))
		.finally(() => {
			checking = false;
			flushSessionCheck();
		});
}

/**
 * observedFetch wraps fetch for the API calls of pages and forms. A 401 of an `/api/` path on this
 * origin asks for a session check. A request that started in an older generation, or during a
 * session check, asks for nothing: a page that answers 401 for another reason must not cause a loop.
 */
export function observedFetch(fetch: typeof globalThis.fetch): typeof globalThis.fetch {
	return async (input, init) => {
		const started = session.generation;
		const duringCheck = checking;
		const res = await fetch(input, init);
		if (res.status === 401 && !duringCheck && started === session.generation && !session.signingOut && ownAPI(input)) {
			recheckSession();
		}
		return res;
	};
}

function ownAPI(input: RequestInfo | URL): boolean {
	const url = new URL(input instanceof Request ? input.url : String(input), location.origin);
	return url.origin === location.origin && url.pathname.startsWith('/api/');
}

/** announceSessionChange tells the other tabs of this origin to read the session again. */
export function announceSessionChange(): void {
	const target = channel ?? openChannel();
	target?.postMessage({ type: 'session-changed' });
	if (target !== channel) target?.close();
}
