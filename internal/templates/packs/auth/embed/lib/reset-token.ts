// Managed by spinup: `spinup upgrade` keeps this file current.
// The token of a password reset link (ADR 0039). The mail holds the link /reset/confirm#<token>.
// A browser never sends the fragment to a server, so no access log, trace, proxy, or Referer header
// receives the token. This module reads it once, keeps it in memory, and removes it from the address.

/** The parts of window.location that takeResetToken reads. */
export type TokenLocation = { hash: string; pathname: string; search: string };

/** The part of window.history that takeResetToken uses. */
export type TokenHistory = { state: unknown; replaceState(data: unknown, unused: string, url?: string | null): void };

// A token is the base64url text of 32 random bytes. The Go API checks the exact value.
const TOKEN = /^[A-Za-z0-9_-]+$/;

// The token that the last fragment held. It stays in memory only, until the document goes away.
let held: string | null = null;

/**
 * takeResetToken reads the token from the fragment and removes the fragment from the address bar
 * at once, with the same path and query. It holds the token in memory and returns it again when
 * the fragment is already gone, so a load that runs again gets the same token. A new fragment
 * replaces the held token. It returns null when no fragment came, or when the fragment does not
 * look like a token. A reload or a copied address has no token.
 */
export function takeResetToken(
	location: TokenLocation = window.location,
	history: TokenHistory = window.history
): string | null {
	if (location.hash === '') return held;
	const fragment = location.hash.startsWith('#') ? location.hash.slice(1) : location.hash;
	// Keep the state of the router: only the address changes.
	history.replaceState(history.state, '', location.pathname + location.search);
	held = TOKEN.test(fragment) ? fragment : null;
	return held;
}

// The root session module imports this module, so this code runs before the router reads the
// address of the first document. The router then never sees the token: page.url has no fragment,
// and the history entry that the router writes after the loads does not put it back in the address
// bar. The page load gets the held token. This also works when the root layout hides the page
// behind its session error card.
if (typeof window !== 'undefined' && window.location?.pathname === '/reset/confirm') takeResetToken();
