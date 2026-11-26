// Managed by spinup: `spinup upgrade` keeps this file current.
// The tests of lib/auth.ts. The safeRedirect cases are those of node mode.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
	loadFlags,
	loadSession,
	loginHref,
	postAuth,
	requireGuest,
	requireRole,
	requireUser,
	safeRedirect
} from './auth.js';

// Exercise a resource home instead of the empty app's root.
vi.mock('#lib/nav.gen.js', () => ({ home: '/workspace' }));

const ada = { id: 1, email: 'ada@example.com', role: 'staff' };

// The helpers log each failure. Keep the test output quiet.
beforeEach(() => {
	vi.spyOn(console, 'error').mockImplementation(() => {});
	vi.spyOn(console, 'warn').mockImplementation(() => {});
});

afterEach(() => {
	vi.restoreAllMocks();
});

function response(status: number, body?: unknown): Response {
	const headers = new Headers();
	if (body !== undefined) headers.set('content-type', 'application/problem+json');
	return new Response(body === undefined ? null : JSON.stringify(body), { status, headers });
}

function reply(res: Response): typeof globalThis.fetch {
	return (async () => res) as unknown as typeof globalThis.fetch;
}

function thrown(run: () => unknown): unknown {
	try {
		run();
	} catch (reason) {
		return reason;
	}
	throw new Error('Expected a throw');
}

describe('safeRedirect', () => {
	it.each([
		['/todos', '/todos'],
		['/todos?page=2', '/todos?page=2'],
		['//evil.com', '/home'],
		['/\\evil.com', '/home'],
		['https://evil.com', '/home'],
		['javascript:alert(1)', '/home'],
		['', '/home'],
		[null, '/home'],
		['/%2fevil.com', '/home'],
		['/%5cevil.com', '/home'],
		['/todos\n', '/home'],
		['/todos%0d%0aLocation:evil', '/home'],
		['/%zz', '/home']
	])('validates %j', (target, expected) => {
		expect(safeRedirect(target, '/home')).toBe(expected);
	});
});

describe('loginHref', () => {
	it('encodes the path and the query once', () => {
		expect(loginHref(new URL('https://app.example.com/todos?page=2'))).toBe('/login?redirectTo=%2Ftodos%3Fpage%3D2');
	});

	it('never nests a return target in the sign-in pages', () => {
		expect(loginHref(new URL('https://app.example.com/login?redirectTo=%2Ftodos'))).toBe('/login?redirectTo=%2Ftodos');
		expect(loginHref(new URL('https://app.example.com/register?redirectTo=%2Ftodos'))).toBe('/login?redirectTo=%2Ftodos');
		expect(loginHref(new URL('https://app.example.com/login'))).toBe('/login');
		expect(loginHref(new URL('https://app.example.com/register?x=1'))).toBe('/login');
	});
});

describe('requireUser', () => {
	it('preserves the protected path and query when redirecting to login', () => {
		expect(thrown(() => requireUser(null, new URL('https://app.example.com/todos?page=2')))).toMatchObject({
			status: 303,
			location: '/login?redirectTo=%2Ftodos%3Fpage%3D2'
		});
	});

	it('returns the signed-in user', () => {
		expect(requireUser(ada, new URL('https://app.example.com/account'))).toBe(ada);
	});
});

describe('requireRole', () => {
	it('returns a user with a listed role', () => {
		const manager = { ...ada, role: 'manager' };
		expect(requireRole(manager, new URL('https://app.example.com/admin'), 'owner', 'manager')).toBe(manager);
	});

	it('shows 403 for a user with another role', () => {
		expect(thrown(() => requireRole(ada, new URL('https://app.example.com/admin'), 'manager'))).toMatchObject({
			status: 403,
			body: { message: 'Your role cannot open this page. Try: sign in with an account that has a required role.' }
		});
	});

	it('sends a signed-out visitor to sign-in first', () => {
		expect(thrown(() => requireRole(null, new URL('https://app.example.com/admin/flags'), 'manager'))).toMatchObject({
			status: 303,
			location: '/login?redirectTo=%2Fadmin%2Fflags'
		});
	});
});

describe('requireGuest', () => {
	it('sends a signed-in visitor home', () => {
		expect(thrown(() => requireGuest(ada, new URL('https://app.example.com/login?redirectTo=%2Ftodos')))).toMatchObject({
			status: 303,
			location: '/workspace'
		});
	});

	it.each([
		['/todos?page=2', '/todos?page=2'],
		['//evil.com', '/workspace'],
		['https://evil.com', '/workspace'],
		['/%2fevil.com', '/workspace'],
		[null, '/workspace']
	])('returns a safe destination for %j', (target, expected) => {
		const url = new URL('https://app.example.com/login');
		if (target !== null) url.searchParams.set('redirectTo', target);
		expect(requireGuest(null, url)).toBe(expected);
	});
});

describe('postAuth', () => {
	it('posts JSON to the same origin and reports success', async () => {
		let seen: Request | undefined;
		const fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
			seen = new Request(new URL(String(input), 'https://app.example.com'), init);
			return response(200);
		};
		const result = await postAuth(fetch as typeof globalThis.fetch, '/api/auth/login', {
			email: 'a@example.com',
			password: 'secret secret'
		});
		expect(result).toEqual({ ok: true });
		expect(seen?.url).toBe('https://app.example.com/api/auth/login');
		expect(seen?.method).toBe('POST');
		expect(seen?.credentials).toBe('same-origin');
		expect(seen?.headers.get('content-type')).toBe('application/json');
		expect(await seen?.json()).toEqual({ email: 'a@example.com', password: 'secret secret' });
	});

	it('sends no body without one', async () => {
		const fetch = vi.fn(async () => response(204));
		expect(await postAuth(fetch, '/api/auth/logout')).toEqual({ ok: true });
		expect(fetch).toHaveBeenCalledWith('/api/auth/logout', {
			method: 'POST',
			credentials: 'same-origin',
			headers: { accept: 'application/json' },
			body: undefined
		});
	});

	it('keeps the first message of each known field', async () => {
		const invalid = await postAuth(
			reply(
				response(422, {
					detail: 'The request has invalid fields.',
					errors: [
						{ field: 'password', message: 'Use at least 12 characters.' },
						{ field: 'password', message: 'second' },
						{ field: 'email', message: 'Enter an email address.' }
					]
				})
			),
			'/api/auth/register',
			{}
		);
		expect(invalid).toEqual({
			ok: false,
			status: 422,
			errors: { password: 'Use at least 12 characters.', email: 'Enter an email address.' }
		});
	});

	it('shows the detail as a form error when no field matches', async () => {
		const conflict = await postAuth(
			reply(response(422, { detail: 'The request has invalid fields.', errors: [{ field: 'name', message: 'x' }] })),
			'/api/auth/register',
			{}
		);
		expect(conflict).toEqual({ ok: false, status: 422, errors: { form: 'The request has invalid fields.' } });

		const wrong = await postAuth(reply(response(401, { detail: 'The email address or password is incorrect.' })), '/api/auth/login', {});
		expect(wrong).toEqual({ ok: false, status: 401, errors: { form: 'The email address or password is incorrect.' } });
	});

	it('keeps the detail of a 429 answer', async () => {
		const detail = 'Too many sign-in attempts from your network. Try again in 12 seconds.';
		expect(await postAuth(reply(response(429, { detail })), '/api/auth/login', {})).toEqual({
			ok: false,
			status: 429,
			errors: { form: detail }
		});
	});

	it('hides the detail of a server error', async () => {
		const broken = await postAuth(reply(response(500, { detail: 'internal' })), '/api/auth/login', {});
		expect(broken).toEqual({ ok: false, status: 500, errors: { form: 'Something went wrong on the server. Try again later.' } });
	});

	it('keeps the detail of a busy 503, with a fallback', async () => {
		const busy = await postAuth(reply(response(503, { detail: 'All slots are taken. Try again in a moment.' })), '/api/auth/login', {});
		expect(busy).toEqual({ ok: false, status: 503, errors: { form: 'All slots are taken. Try again in a moment.' } });
		const bare = await postAuth(reply(response(503)), '/api/auth/login', {});
		expect(bare).toEqual({ ok: false, status: 503, errors: { form: 'The server is busy. Try again in a moment.' } });
	});

	it('names the status of a problem without detail', async () => {
		expect(await postAuth(reply(response(400)), '/api/auth/login', {})).toEqual({
			ok: false,
			status: 400,
			errors: { form: 'The request failed (400).' }
		});
	});

	it('never throws when the API is down', async () => {
		const down = (async () => {
			throw new TypeError('fetch failed');
		}) as unknown as typeof globalThis.fetch;
		expect(await postAuth(down, '/api/auth/logout')).toEqual({
			ok: false,
			status: 503,
			errors: { form: 'The server is not available. Try again later.' }
		});
	});
});

describe('loadSession', () => {
	it('returns the user of the session answer, and only its known fields', async () => {
		const fetch = vi.fn(reply(Response.json({ user: { ...ada, token: 'x' } })));
		expect(await loadSession(fetch)).toEqual(ada);
		expect(fetch).toHaveBeenCalledWith('/api/session', expect.objectContaining({ credentials: 'same-origin' }));
	});

	it('treats only a 401 as a signed-out visitor', async () => {
		expect(await loadSession(reply(response(401, { detail: 'No user is signed in.' })))).toBeNull();
	});

	it.each([
		['a server error', reply(response(500))],
		['a missing route', reply(response(404))],
		['an answer without a user', reply(Response.json({}))],
		['an answer that is not JSON', reply(new Response('<html>', { status: 200 }))],
		[
			'a transport failure',
			(async () => {
				throw new TypeError('fetch failed');
			}) as unknown as typeof globalThis.fetch
		]
	])('fails closed on %s', async (_name, fetch) => {
		await expect(loadSession(fetch)).rejects.toMatchObject({ status: 503 });
	});
});

describe('loadFlags', () => {
	it('returns the boolean flags of the answer', async () => {
		expect(await loadFlags(reply(Response.json({ flags: { beta: true, old: false, bad: 'yes' } })))).toEqual({
			beta: true,
			old: false
		});
	});

	it('warns and returns no flags on a failure', async () => {
		const warn = vi.mocked(console.warn);
		expect(await loadFlags(reply(response(500)))).toEqual({});
		const down = (async () => {
			throw new TypeError('fetch failed');
		}) as unknown as typeof globalThis.fetch;
		expect(await loadFlags(down)).toEqual({});
		expect(warn).toHaveBeenCalledTimes(2);
	});
});
