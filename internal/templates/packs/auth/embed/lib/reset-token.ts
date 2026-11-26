// Managed by spinup: `spinup upgrade` keeps this file current.
// The token of a password reset link (ADR 0039). The mail holds the link /reset/confirm#<token>.
// A browser never sends the fragment to a server, so no access log, trace, proxy, or Referer header
// receives the token. The page reads it once, keeps it in memory, and removes it from the address.

/** The parts of window.location that takeResetToken reads. */
export type TokenLocation = { hash: string; pathname: string; search: string };

/** The part of window.history that takeResetToken uses. */
export type TokenHistory = { state: unknown; replaceState(data: unknown, unused: string, url?: string | null): void };

// A token is the base64url text of 32 random bytes. The Go API checks the exact value.
const TOKEN = /^[A-Za-z0-9_-]+$/;

/**
 * takeResetToken reads the token from the fragment and removes the fragment from the address bar
 * at once, with the same path and query. It returns null when the fragment is empty or does not
 * look like a token. Call it one time when the page starts. A reload or a copied address has no token.
 */
export function takeResetToken(
	location: TokenLocation = window.location,
	history: TokenHistory = window.history
): string | null {
	const fragment = location.hash.startsWith('#') ? location.hash.slice(1) : location.hash;
	if (location.hash === '') return null;
	// Keep the state of the router: only the address changes.
	history.replaceState(history.state, '', location.pathname + location.search);
	return TOKEN.test(fragment) ? fragment : null;
}
