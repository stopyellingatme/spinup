// Managed by spinup: `spinup upgrade` keeps this file current.
// The tests of lib/reset-token.ts: the read-and-strip step, the held token, the visit of the reset
// page, the cases without a token, and the take at import. Each test gets a new module, so no token
// stays from another test.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { TokenHistory } from './reset-token.js';

const token = 'Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZmdoaWo';
const other = 'b3RoZXJ0b2tlbmZvcmFub3RoZXJsaW5rMDEyMzQ1Njc';

let takeResetToken: typeof import('./reset-token.js').takeResetToken;
let followResetVisit: typeof import('./reset-token.js').followResetVisit;

beforeEach(async () => {
	vi.resetModules();
	({ takeResetToken, followResetVisit } = await import('./reset-token.js'));
});

afterEach(() => {
	vi.unstubAllGlobals();
});

function fixture(hash: string, search = '') {
	const calls: { state: unknown; url: string | null | undefined }[] = [];
	const location = { hash, pathname: '/reset/confirm', search };
	const history: TokenHistory = {
		state: { 'sveltekit:history': 3 },
		replaceState: (state, _unused, url) => {
			calls.push({ state, url });
			location.hash = '';
		}
	};
	return { location, history, calls };
}

describe('takeResetToken', () => {
	it('returns the token and removes the fragment', () => {
		const { location, history, calls } = fixture('#' + token);
		expect(takeResetToken(location, history)).toBe(token);
		expect(calls).toEqual([{ state: { 'sveltekit:history': 3 }, url: '/reset/confirm' }]);
	});

	it('returns the held token again when the fragment is gone', () => {
		const { location, history, calls } = fixture('#' + token);
		expect(takeResetToken(location, history)).toBe(token);
		expect(location.hash).toBe('');
		// The load runs again, for example after a retry of the session read.
		expect(takeResetToken(location, history)).toBe(token);
		expect(calls).toHaveLength(1);
	});

	it('replaces the held token with the token of a new fragment', () => {
		const first = fixture('#' + token);
		expect(takeResetToken(first.location, first.history)).toBe(token);
		const second = fixture('#' + other);
		expect(takeResetToken(second.location, second.history)).toBe(other);
		expect(takeResetToken(second.location, second.history)).toBe(other);
		// A fragment that is not a token replaces the held token too: that link is not valid.
		const bad = fixture('#a/b');
		expect(takeResetToken(bad.location, bad.history)).toBeNull();
		expect(takeResetToken(bad.location, bad.history)).toBeNull();
	});

	it('keeps the path and the query', () => {
		const { location, history, calls } = fixture('#' + token, '?lang=en');
		expect(takeResetToken(location, history)).toBe(token);
		expect(calls.map((call) => call.url)).toEqual(['/reset/confirm?lang=en']);
	});

	it('returns null and changes nothing without a fragment', () => {
		const { location, history, calls } = fixture('');
		expect(takeResetToken(location, history)).toBeNull();
		expect(takeResetToken(location, history)).toBeNull();
		expect(calls).toEqual([]);
	});

	it('returns null and still removes a fragment that is not a token', () => {
		for (const hash of ['#', '#a b', '#a/b', '#a%2Fb', '#token?x=1']) {
			const { location, history, calls } = fixture(hash);
			expect(takeResetToken(location, history), hash).toBeNull();
			expect(calls.length, hash).toBe(1);
		}
	});
});

describe('the visit of the reset page', () => {
	it('drops the token when a navigation leaves the page', () => {
		const first = fixture('#' + token);
		expect(takeResetToken(first.location, first.history)).toBe(token);
		// The root layout calls followResetVisit after the navigation to /reset. The session error
		// card can hide the page at that time: the token goes away all the same.
		const away = fixture('');
		away.location.pathname = '/reset';
		followResetVisit(away.location);
		// A later visit without a fragment gets no token, so the page shows the invalid-link message.
		const later = fixture('');
		expect(takeResetToken(later.location, later.history)).toBeNull();
	});

	it('keeps the token while the address stays on the page', () => {
		const { location, history } = fixture('#' + token);
		expect(takeResetToken(location, history)).toBe(token);
		// The first navigation of the document ends on the page. A retry of the session read is not
		// a navigation at all.
		followResetVisit(location);
		expect(takeResetToken(location, history)).toBe(token);
	});
});

describe('the take at import', () => {
	async function load(pathname: string, hash: string) {
		const { location, history, calls } = fixture(hash);
		location.pathname = pathname;
		vi.stubGlobal('window', { location, history });
		vi.resetModules();
		const module = await import('./reset-token.js');
		return { module, location, history, calls };
	}

	it('takes the token of the reset page before the router reads the address', async () => {
		const { module, location, history, calls } = await load('/reset/confirm', '#' + token);
		expect(calls.map((call) => call.url)).toEqual(['/reset/confirm']);
		expect(module.takeResetToken(location, history)).toBe(token);
	});

	it('leaves the fragment of another page alone', async () => {
		const { location, calls } = await load('/docs', '#install');
		expect(calls).toEqual([]);
		expect(location.hash).toBe('#install');
	});
});
