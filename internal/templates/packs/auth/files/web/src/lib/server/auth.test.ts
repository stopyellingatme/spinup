// Managed by spinup: `spinup upgrade` keeps this file current.
import type { Action, Cookies, ServerLoad } from '@sveltejs/kit';
import { describe, expect, it, vi } from 'vitest';
import { home } from '#lib/nav.gen.js';
import { actions as loginActions, load as loginLoad } from '../../routes/login/+page.server.js';
import { actions as registerActions, load as registerLoad } from '../../routes/register/+page.server.js';
import { actions as logoutActions } from '../../routes/logout/+page.server.js';
import { forwardSessionCookie, postAuth, requireUser, safeRedirect } from './auth.js';

// Exercise a resource home instead of the empty app's root.
vi.mock('#lib/nav.gen.js', () => ({ home: '/workspace' }));

type Call = [op: 'set' | 'delete', name: string, value: string | undefined, options: object];

/** fakeCookies records set and delete calls. parse reads the attributes that Go writes. */
function fakeCookies(): { cookies: Cookies; calls: Call[] } {
	const calls: Call[] = [];
	const parse = (header: string) => {
		const [pair, ...attrs] = header.split(';').map((s) => s.trim());
		const eq = pair.indexOf('=');
		const cookie: Record<string, unknown> = { name: pair.slice(0, eq), value: pair.slice(eq + 1) };
		for (const attr of attrs) {
			const [key, val] = attr.split('=');
			switch (key.toLowerCase()) {
				case 'path':
					cookie.path = val;
					break;
				case 'max-age':
					cookie.maxAge = Number(val);
					break;
				case 'httponly':
					cookie.httpOnly = true;
					break;
				case 'secure':
					cookie.secure = true;
					break;
				case 'samesite':
					cookie.sameSite = val.toLowerCase();
					break;
			}
		}
		return cookie;
	};
	const cookies = {
		parse,
		set: (name: string, value: string, options: object) => calls.push(['set', name, value, options]),
		delete: (name: string, options: object) => calls.push(['delete', name, undefined, options])
	} as unknown as Cookies;
	return { cookies, calls };
}

function response(status: number, setCookies: string[] = [], body?: unknown): Response {
	const headers = new Headers();
	for (const c of setCookies) headers.append('set-cookie', c);
	if (body !== undefined) headers.set('content-type', 'application/problem+json');
	return new Response(body === undefined ? null : JSON.stringify(body), { status, headers });
}

describe('forwardSessionCookie', () => {
	it('copies the Go session cookie with its attributes', () => {
		const { cookies, calls } = fakeCookies();
		forwardSessionCookie(
			response(200, [
				'session=abc123; Path=/; Expires=Thu, 22 Oct 2026 12:00:00 GMT; Max-Age=604800; HttpOnly; Secure; SameSite=Lax',
				'other=x; Path=/'
			]),
			cookies
		);
		expect(calls).toEqual([
			['set', 'session', 'abc123', { path: '/', httpOnly: true, secure: true, sameSite: 'lax', maxAge: 604800 }]
		]);
	});

	it('deletes the browser cookie when Go expires it', () => {
		const { cookies, calls } = fakeCookies();
		forwardSessionCookie(
			response(204, ['session=; Path=/; Expires=Thu, 01 Jan 1970 00:00:01 GMT; Max-Age=0; HttpOnly; SameSite=Lax']),
			cookies
		);
		expect(calls).toEqual([['delete', 'session', undefined, { path: '/', httpOnly: true, secure: false, sameSite: 'lax' }]]);
	});
});

describe('postAuth', () => {
	it('posts JSON to the public API prefix and reports success', async () => {
		const { cookies, calls } = fakeCookies();
		let seen: Request | undefined;
		const fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
			seen = new Request(input, init);
			return response(200, ['session=t1; Path=/; Max-Age=60; HttpOnly; SameSite=Lax']);
		};
		const result = await postAuth(fetch as typeof globalThis.fetch, cookies, '/api/auth/login', {
			email: 'a@example.com',
			password: 'secret secret'
		});
		expect(result).toEqual({ ok: true });
		expect(seen?.url).toBe(`${__PUBLIC_ORIGIN__}/api/auth/login`);
		expect(seen?.method).toBe('POST');
		expect(seen?.headers.get('content-type')).toBe('application/json');
		expect(await seen?.json()).toEqual({ email: 'a@example.com', password: 'secret secret' });
		expect(calls.map((c) => c[2])).toEqual(['t1']);
	});

	it('turns field errors and problem details into messages', async () => {
		const { cookies } = fakeCookies();
		const reply = (res: Response) => (async () => res) as unknown as typeof globalThis.fetch;
		const invalid = await postAuth(
			reply(response(422, [], { detail: 'The request has invalid fields.', errors: [{ field: 'password', message: 'Use at least 12 characters.' }] })),
			cookies,
			'/api/auth/register',
			{ email: 'a@example.com', password: 'short' }
		);
		expect(invalid).toEqual({ ok: false, status: 422, errors: { password: 'Use at least 12 characters.' } });

		const wrong = await postAuth(reply(response(401, [], { detail: 'The email address or password is incorrect.' })), cookies, '/api/auth/login', {});
		expect(wrong).toEqual({ ok: false, status: 401, errors: { form: 'The email address or password is incorrect.' } });

		const broken = await postAuth(reply(response(500, [], { detail: 'internal' })), cookies, '/api/auth/login', {});
		expect(broken).toMatchObject({ ok: false, status: 500 });
		expect(broken.ok === false && broken.errors.form).not.toContain('internal');

		const busy = await postAuth(reply(response(503, [], { detail: 'The server is busy. Try again in a moment.' })), cookies, '/api/auth/login', {});
		expect(busy).toEqual({ ok: false, status: 503, errors: { form: 'The server is busy. Try again in a moment.' } });
	});

	it('never throws when the API is down', async () => {
		const { cookies } = fakeCookies();
		const down = (async () => {
			throw new TypeError('fetch failed');
		}) as unknown as typeof globalThis.fetch;
		expect(await postAuth(down, cookies, '/api/auth/logout')).toMatchObject({ ok: false, status: 503 });
	});
});

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

describe('requireUser', () => {
	it('preserves the protected path and query when redirecting to login', () => {
		try {
			requireUser({ user: null }, new URL('https://app.example.com/todos?page=2'));
			throw new Error('Expected a redirect');
		} catch (error) {
			expect(error).toMatchObject({ status: 303, location: '/login?redirectTo=%2Ftodos%3Fpage%3D2' });
		}
	});

	it('returns the signed-in user', () => {
		const user = { id: 1, email: 'ada@example.com' };
		expect(requireUser({ user }, new URL('https://app.example.com/account'))).toBe(user);
	});
});

describe.each([
	['login', loginLoad as ServerLoad, loginActions.default as Action],
	['register', registerLoad as ServerLoad, registerActions.default as Action]
] as const)('%s redirects', (route, load, submit) => {
	it.each(['/todos?page=2', '//evil.com', null])('validates the destination %j after success', async (target) => {
		const url = new URL(`https://app.example.com/${route}`);
		if (target !== null) url.searchParams.set('redirectTo', target);
		const { cookies } = fakeCookies();
		const event = {
			url,
			cookies,
			fetch: async () => response(200),
			request: new Request(url, {
				method: 'POST',
				body: new URLSearchParams({ email: 'ada@example.com', password: 'correct horse battery' })
			})
		} as unknown as Parameters<typeof submit>[0];
		await expect(submit(event)).rejects.toMatchObject({
			status: 303,
			location: target === '/todos?page=2' ? target : home
		});
	});

	it('sends signed-in users to home', () => {
		const event = {
			locals: { user: { id: 1, email: 'ada@example.com' } },
			url: new URL(`https://app.example.com/${route}?redirectTo=/todos`)
		} as Parameters<typeof load>[0];
		try {
			load(event);
			throw new Error('Expected a redirect');
		} catch (error) {
			expect(error).toMatchObject({ status: 303, location: home });
		}
	});

	it.each(['/todos?page=2', '//evil.com', null])('supplies a safe destination %j to the page', async (target) => {
		const url = new URL(`https://app.example.com/${route}`);
		if (target !== null) url.searchParams.set('redirectTo', target);
		const event = { locals: { user: null }, url } as Parameters<typeof load>[0];
		expect(await load(event)).toEqual({ redirectTo: target === '/todos?page=2' ? target : home });
	});
});

it('redirects to login after logout', async () => {
	const { cookies } = fakeCookies();
	const event = {
		cookies,
		fetch: async () => response(204),
		url: new URL('https://app.example.com/logout')
	} as unknown as Parameters<typeof logoutActions.default>[0];
	await expect(logoutActions.default(event)).rejects.toMatchObject({ status: 303, location: '/login' });
});
