// Managed by spinup: `spinup upgrade` keeps this file current.
// The tests of lib/reset-token.ts: the read-and-strip step, and the cases without a token.
import { describe, expect, it } from 'vitest';
import { takeResetToken, type TokenHistory } from './reset-token.js';

const token = 'Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZmdoaWo';

function fixture(hash: string, search = '') {
	const calls: { state: unknown; url: string | null | undefined }[] = [];
	const history: TokenHistory = {
		state: { 'sveltekit:history': 3 },
		replaceState: (state, _unused, url) => void calls.push({ state, url })
	};
	return { location: { hash, pathname: '/reset/confirm', search }, history, calls };
}

describe('takeResetToken', () => {
	it('returns the token and removes the fragment', () => {
		const { location, history, calls } = fixture('#' + token);
		expect(takeResetToken(location, history)).toBe(token);
		expect(calls).toEqual([{ state: { 'sveltekit:history': 3 }, url: '/reset/confirm' }]);
	});

	it('keeps the path and the query', () => {
		const { location, history, calls } = fixture('#' + token, '?lang=en');
		expect(takeResetToken(location, history)).toBe(token);
		expect(calls.map((call) => call.url)).toEqual(['/reset/confirm?lang=en']);
	});

	it('returns null and changes nothing without a fragment', () => {
		const { location, history, calls } = fixture('');
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
