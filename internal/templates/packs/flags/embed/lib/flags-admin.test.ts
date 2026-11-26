// Managed by spinup: `spinup upgrade` keeps this file current.
// The tests of lib/flags-admin.ts: the pending key of each admin form and its button.
import { describe, expect, it, vi } from 'vitest';
import { pendingKey } from './flags-admin.js';

vi.mock('#lib/session-watch.js', () => ({ observedFetch: (fetch: typeof globalThis.fetch) => fetch }));

describe('pendingKey', () => {
	it('gives the add form one key, whatever email address it holds', () => {
		// The form submits with the typed address. Its button asks with no address.
		expect(pendingKey('user', 'beta', 'ada@example.com')).toBe(pendingKey('user', 'beta'));
	});

	it('gives the rule form one key', () => {
		expect(pendingKey('rule', 'beta', '')).toBe(pendingKey('rule', 'beta'));
	});

	it('gives each clear button the key of its account', () => {
		expect(pendingKey('clearUser', 'beta', 'ada@example.com')).not.toBe(pendingKey('clearUser', 'beta', 'bob@example.com'));
		expect(pendingKey('clearUser', 'beta', 'ada@example.com')).toBe(pendingKey('clearUser', 'beta', 'ada@example.com'));
	});

	it('keeps the forms and the flags apart', () => {
		const keys = [
			pendingKey('rule', 'beta'),
			pendingKey('user', 'beta'),
			pendingKey('clearUser', 'beta', ''),
			pendingKey('rule', 'gamma'),
			pendingKey('user', 'gamma')
		];
		expect(new Set(keys).size).toBe(keys.length);
	});
});
