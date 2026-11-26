// Managed by spinup: `spinup upgrade` keeps this file current.
// The tests of lib/sign-out.ts and the sign-out pause of lib/session.svelte.ts. One list records
// the stop callbacks, the logout request, the announcement, and the document change in order.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ invalidate: vi.fn(), navigating: { to: null as unknown }, toast: vi.fn() }));
vi.mock('$app/navigation', () => ({ invalidate: mocks.invalidate }));
vi.mock('$app/state', () => ({ navigating: mocks.navigating }));
vi.mock('svelte-sonner', () => ({ toast: { error: mocks.toast } }));
vi.mock('#lib/nav.gen.js', () => ({ home: '/workspace' }));

type Fetch = typeof globalThis.fetch;
const ada = { id: 1, email: 'ada@example.com', role: 'staff' };
const origin = 'https://app.example.com';

let log: string[];
let answers: (() => Promise<Response>)[];
let channels: { receive: (data: unknown) => void }[];
let watch: typeof import('./session-watch.js');
let state: typeof import('./session.svelte.js');
let signOut: typeof import('./sign-out.js').signOut;

beforeEach(async () => {
	vi.resetModules();
	log = [];
	answers = [];
	channels = [];
	mocks.invalidate.mockReset();
	mocks.invalidate.mockResolvedValue(undefined);
	mocks.toast.mockReset();
	mocks.navigating.to = null;
	const location = { origin, replace: (target: string) => log.push(`replace ${target}`) };
	vi.stubGlobal('location', location);
	vi.stubGlobal('window', Object.assign(new EventTarget(), { location }));
	vi.stubGlobal('document', Object.assign(new EventTarget(), { visibilityState: 'visible' }));
	vi.stubGlobal('sessionStorage', { getItem: () => null, setItem: () => {}, removeItem: () => {} });
	vi.stubGlobal(
		'BroadcastChannel',
		class {
			onmessage: ((event: MessageEvent) => void) | null = null;
			constructor() {
				channels.push({ receive: (data) => this.onmessage?.({ data } as MessageEvent) });
			}
			postMessage(data: unknown) {
				log.push(`announce ${JSON.stringify(data)}`);
			}
			close() {}
		}
	);
	// The logout endpoint answers with the next queued answer, or 204.
	vi.stubGlobal(
		'fetch',
		vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
			log.push(`${init?.method ?? 'GET'} ${String(input)}`);
			return (answers.shift() ?? (async () => new Response(null, { status: 204 })))();
		})
	);
	vi.spyOn(console, 'warn').mockImplementation(() => {});
	vi.spyOn(console, 'error').mockImplementation(() => {});
	watch = await import('./session-watch.js');
	state = await import('./session.svelte.js');
	({ signOut } = await import('./sign-out.js'));
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

/** hold queues a logout answer that the test gives later. */
function hold(): (res: Response) => void {
	let give: (res: Response) => void = () => {};
	answers.push(() => new Promise<Response>((resolve) => (give = resolve)));
	return (res) => give(res);
}

function feed(name: string): () => void {
	return state.onSessionPause(
		() => log.push(`stop ${name}`),
		() => log.push(`resume ${name}`)
	);
}

async function ticks(n = 20) {
	for (let i = 0; i < n; i++) await Promise.resolve();
}

async function pending(promise: Promise<unknown>): Promise<boolean> {
	let done = false;
	promise.then(
		() => (done = true),
		() => (done = true)
	);
	await ticks();
	return !done;
}

const session = (user: App.User | null): Fetch =>
	(async (input: RequestInfo | URL) =>
		String(input) === '/api/session'
			? user
				? Response.json({ user })
				: new Response(null, { status: 401 })
			: Response.json({ flags: {} })) as unknown as Fetch;

describe('signOut', () => {
	it('stops every stream before the logout request, then leaves for /login', async () => {
		await state.readSession(session(ada));
		feed('todos');
		feed('reports');
		const done = signOut();
		// The stops and the request happen before signOut returns: no await comes first.
		expect(log).toEqual(['stop todos', 'stop reports', 'POST /api/auth/logout']);
		expect(await done).toBe(true);
		expect(log).toEqual([
			'stop todos',
			'stop reports',
			'POST /api/auth/logout',
			'announce {"type":"session-changed"}',
			'replace /login'
		]);
		expect(state.session.data).toEqual({ user: null, flags: {} });
	});

	it('sends the request after a stop callback that throws, and stops the others', async () => {
		state.onSessionPause(() => {
			log.push('stop broken');
			throw new Error('boom');
		}, vi.fn());
		feed('todos');
		expect(await signOut()).toBe(true);
		expect(log.slice(0, 3)).toEqual(['stop broken', 'stop todos', 'POST /api/auth/logout']);
	});

	it('gives duplicate calls the same attempt', async () => {
		const give = hold();
		const first = signOut();
		const second = signOut();
		expect(second).toBe(first);
		give(new Response(null, { status: 204 }));
		expect(await first).toBe(true);
		expect(await signOut()).toBe(true);
		expect(log.filter((line) => line.startsWith('POST'))).toHaveLength(1);
	});

	it('keeps the pause through the departure', async () => {
		feed('todos');
		expect(await signOut()).toBe(true);
		expect(state.session.signingOut).toBe(true);
		expect(state.session.changingUser).toBe(true);
		feed('late');
		expect(log.slice(-1)).toEqual(['stop late']);
		// A root load after the departure gets no data, so no old guard can redirect.
		const read = vi.fn(session(null));
		expect(await pending(state.readSession(read))).toBe(true);
		expect(read).not.toHaveBeenCalled();
		expect(log).not.toContain('resume todos');
	});

	it('starts a registration during the pause paused, and resumes it after a failure', async () => {
		feed('todos');
		const give = hold();
		const done = signOut();
		feed('late');
		expect(log).toEqual(['stop todos', 'POST /api/auth/logout', 'stop late']);
		give(new Response(null, { status: 500 }));
		expect(await done).toBe(false);
		expect(log.slice(3)).toEqual(['resume todos', 'resume late']);
	});

	it('does not stop or resume a removed registration', async () => {
		feed('todos')();
		answers.push(async () => new Response(null, { status: 500 }));
		expect(await signOut()).toBe(false);
		expect(log).toEqual(['POST /api/auth/logout']);
	});

	it.each([
		['a server error', async () => new Response(null, { status: 500 }), 'Sign-out failed. Something went wrong on the server. Try again later.'],
		[
			'a transport failure',
			async () => {
				throw new TypeError('fetch failed');
			},
			'Sign-out failed. The server is not available. Try again later.'
		],
		[
			'a busy API',
			async () => Response.json({ detail: 'The server is busy. Try again in a moment.' }, { status: 503 }),
			'Sign-out failed. The server is busy. Try again in a moment.'
		],
		[
			'an answer with only a field error',
			async () => Response.json({ errors: [{ field: 'email', message: 'x' }] }, { status: 422 }),
			'Sign-out failed. Try again.'
		]
	])('keeps the session after %s and shows why', async (_name, reply, message) => {
		await state.readSession(session(ada));
		const generation = state.session.generation;
		answers.push(reply);
		expect(await signOut()).toBe(false);
		expect(mocks.toast).toHaveBeenCalledExactlyOnceWith(message);
		expect(log.some((line) => line.startsWith('replace'))).toBe(false);
		expect(state.session.signingOut).toBe(false);
		expect(state.session.changingUser).toBe(false);
		expect(state.session.data.user).toEqual(ada);
		expect(state.session.generation).toBe(generation + 1);
	});

	it('allows another attempt after a failure', async () => {
		feed('todos');
		answers.push(async () => new Response(null, { status: 500 }));
		expect(await signOut()).toBe(false);
		expect(await signOut()).toBe(true);
		expect(log).toEqual([
			'stop todos',
			'POST /api/auth/logout',
			'resume todos',
			'stop todos',
			'POST /api/auth/logout',
			'announce {"type":"session-changed"}',
			'replace /login'
		]);
	});
});

describe('the pause', () => {
	it('ignores late 401 answers and session requests until the outcome', async () => {
		watch.watchSession();
		const late = vi.fn(async () => new Response(null, { status: 401 })) as unknown as Fetch;
		const give = hold();
		const done = signOut();
		await watch.observedFetch(late)('/api/todos');
		watch.recheckSession();
		expect(mocks.invalidate).not.toHaveBeenCalled();
		give(new Response(null, { status: 204 }));
		await done;
		expect(mocks.invalidate).not.toHaveBeenCalled();
	});

	it('delivers a change from another tab after a failed sign-out', async () => {
		watch.watchSession();
		const give = hold();
		const done = signOut();
		channels[0].receive({ type: 'session-changed' });
		expect(mocks.invalidate).not.toHaveBeenCalled();
		give(new Response(null, { status: 500 }));
		await done;
		expect(mocks.invalidate).toHaveBeenCalledExactlyOnceWith('app:session');
	});

	it('makes a root load wait for the outcome, then read again', async () => {
		await state.readSession(session(ada));
		const give = hold();
		const done = signOut();
		const fetch = vi.fn(session(ada));
		const read = state.readSession(fetch);
		expect(await pending(read)).toBe(true);
		expect(fetch).not.toHaveBeenCalled();
		give(new Response(null, { status: 500 }));
		await done;
		expect((await read).user).toEqual(ada);
		expect(fetch).toHaveBeenCalledWith('/api/session', expect.anything());
	});

	it('reads again when a sign-out starts during a read', async () => {
		await state.readSession(session(ada));
		let first = true;
		let give: (res: Response) => void = () => {};
		const fetch = vi.fn(async (input: RequestInfo | URL) => {
			if (String(input) !== '/api/session') return Response.json({ flags: {} });
			if (!first) return Response.json({ user: ada });
			first = false;
			return new Promise<Response>((resolve) => (give = resolve));
		}) as unknown as Fetch;
		const read = state.readSession(fetch);
		const logout = hold();
		const done = signOut();
		// The old read answers signed out during the sign-out. Its answer belongs to the old generation.
		give(new Response(null, { status: 401 }));
		expect(await pending(read)).toBe(true);
		logout(new Response(null, { status: 500 }));
		await done;
		expect((await read).user).toEqual(ada);
		expect(log.some((line) => line.startsWith('replace'))).toBe(false);
	});
});
