// Managed by spinup: `spinup upgrade` keeps this file current.
// The tests of lib/session.svelte.ts and lib/session-watch.ts: the root read, the identity
// change, and the requests for a new read. Each test gets new modules and new browser fakes.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ invalidate: vi.fn(), navigating: { to: null as unknown } }));
vi.mock('$app/navigation', () => ({ invalidate: mocks.invalidate }));
vi.mock('$app/state', () => ({ navigating: mocks.navigating }));
vi.mock('#lib/nav.gen.js', () => ({ home: '/workspace' }));

// The flags module adds lib/flags.gen.ts. Without it, the root reads no flags.
const hasFlags = Object.keys(import.meta.glob('./flags.gen.ts')).length > 0;

type Fetch = typeof globalThis.fetch;
const ada = { id: 1, email: 'ada@example.com', role: 'staff' };
const origin = 'https://app.example.com';

class FakeChannel {
	static all: FakeChannel[] = [];
	onmessage: ((event: MessageEvent) => void) | null = null;
	closed = false;
	sent: unknown[] = [];
	constructor(readonly name: string) {
		FakeChannel.all.push(this);
	}
	postMessage(data: unknown) {
		this.sent.push(data);
	}
	close() {
		this.closed = true;
	}
	receive(data: unknown) {
		if (!this.closed) this.onmessage?.({ data } as MessageEvent);
	}
}

let watch: typeof import('./session-watch.js');
let state: typeof import('./session.svelte.js');
let doc: EventTarget & { visibilityState: string };
let win: EventTarget;
let storage: Map<string, string>;
let replace: ReturnType<typeof vi.fn>;

beforeEach(async () => {
	vi.resetModules();
	FakeChannel.all = [];
	mocks.invalidate.mockReset();
	mocks.invalidate.mockResolvedValue(undefined);
	mocks.navigating.to = null;
	replace = vi.fn();
	const location = { origin, replace };
	doc = Object.assign(new EventTarget(), { visibilityState: 'visible' });
	win = Object.assign(new EventTarget(), { location });
	storage = new Map();
	vi.stubGlobal('BroadcastChannel', FakeChannel);
	vi.stubGlobal('document', doc);
	vi.stubGlobal('window', win);
	vi.stubGlobal('location', location);
	vi.stubGlobal('sessionStorage', {
		getItem: (key: string) => storage.get(key) ?? null,
		setItem: (key: string, value: string) => storage.set(key, value),
		removeItem: (key: string) => storage.delete(key)
	});
	watch = await import('./session-watch.js');
	state = await import('./session.svelte.js');
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

/** serve answers the session read with user (401 for null) and the flags read with flags. */
function serve(user: App.User | null, flags: Record<string, boolean> = {}): Fetch {
	return vi.fn(async (input: RequestInfo | URL) => {
		if (String(input) === '/api/flags') return Response.json({ flags });
		return user ? Response.json({ user }) : Response.json({ detail: 'No user is signed in.' }, { status: 401 });
	}) as unknown as Fetch;
}

/** held records each request and answers it when the test calls resolve. */
function held() {
	const calls: { url: string; resolve: (res: Response) => void }[] = [];
	const fetch = vi.fn(
		(input: RequestInfo | URL) => new Promise<Response>((resolve) => calls.push({ url: String(input), resolve }))
	) as unknown as Fetch;
	return { fetch, calls };
}

/** answer resolves the held calls of one read: the session and, with flags, the flags. */
function answer(calls: { url: string; resolve: (res: Response) => void }[], user: App.User | null, flags = {}) {
	for (const call of calls.splice(0)) {
		if (call.url === '/api/flags') call.resolve(Response.json({ flags }));
		else call.resolve(user ? Response.json({ user }) : new Response(null, { status: 401 }));
	}
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

function channel(): FakeChannel {
	return FakeChannel.all[0];
}

describe('readSession', () => {
	it('starts the session and flags reads at the same time', async () => {
		const { fetch, calls } = held();
		const read = state.readSession(fetch);
		expect(calls.map((c) => c.url)).toEqual(hasFlags ? ['/api/session', '/api/flags'] : ['/api/session']);
		answer(calls, ada, { beta: true });
		const expected = { user: ada, flags: hasFlags ? { beta: true } : {} };
		expect(await read).toEqual(expected);
		expect(state.session.data).toEqual(expected);
	});

	it('reads the flags again on each root load, for example after a flag save', async () => {
		await state.readSession(serve(ada, { beta: false }));
		const again = serve(ada, { beta: true });
		expect((await state.readSession(again)).flags).toEqual(hasFlags ? { beta: true } : {});
		expect(vi.mocked(again)).toHaveBeenCalledTimes(hasFlags ? 2 : 1);
	});

	it.runIf(hasFlags)('publishes no flags when the flags read fails', async () => {
		vi.spyOn(console, 'warn').mockImplementation(() => {});
		const fetch = vi.fn(async (input: RequestInfo | URL) =>
			String(input) === '/api/flags' ? new Response(null, { status: 500 }) : Response.json({ user: ada })
		) as unknown as Fetch;
		expect(await state.readSession(fetch)).toEqual({ user: ada, flags: {} });
	});

	it('keeps the last accepted identity when a read fails', async () => {
		vi.spyOn(console, 'error').mockImplementation(() => {});
		vi.spyOn(console, 'warn').mockImplementation(() => {});
		await state.readSession(serve(ada));
		const broken = vi.fn(async () => new Response(null, { status: 500 })) as unknown as Fetch;
		await expect(state.readSession(broken)).rejects.toMatchObject({ status: 503 });
		expect(state.session.data.user).toEqual(ada);
		expect(replace).not.toHaveBeenCalled();
	});

	it('gives a superseded read the snapshot of the newest read', async () => {
		const first = held();
		const second = held();
		const older = state.readSession(first.fetch);
		const newer = state.readSession(second.fetch);
		answer(second.calls, ada);
		const snapshot = await newer;
		// The older read answers last, and signed out. It must not win.
		answer(first.calls, null);
		expect(await older).toBe(snapshot);
		expect(state.session.data.user).toEqual(ada);
		expect(replace).not.toHaveBeenCalled();
	});

	it('accepts the first identity without a document change', async () => {
		await state.readSession(serve(ada));
		expect(replace).not.toHaveBeenCalled();
		expect(state.session.changingUser).toBe(false);
	});

	it.each([
		['another id', { ...ada, id: 2 }, '/workspace'],
		['another role', { ...ada, role: 'manager' }, '/workspace'],
		['another email', { ...ada, email: 'bob@example.com' }, '/workspace'],
		['no user', null, '/login']
	])('replaces the document for %s', async (_name, next, target) => {
		await state.readSession(serve(ada, { beta: true }));
		const stop = vi.fn();
		state.onSessionPause(stop, vi.fn());
		const generation = state.session.generation;
		const read = state.readSession(serve(next));
		expect(await pending(read)).toBe(true);
		expect(replace).toHaveBeenCalledExactlyOnceWith(target);
		expect(state.session.changingUser).toBe(true);
		expect(state.session.data).toEqual({ user: null, flags: {} });
		expect(state.session.generation).toBe(generation + 1);
		expect(stop).toHaveBeenCalledOnce();
		// A later read gets no data either: the document is going away.
		const late = serve(ada);
		expect(await pending(state.readSession(late))).toBe(true);
		expect(late).not.toHaveBeenCalled();
	});

	it('takes a signed-out visitor who signs in elsewhere home', async () => {
		await state.readSession(serve(null));
		expect(await pending(state.readSession(serve(ada)))).toBe(true);
		expect(replace).toHaveBeenCalledExactlyOnceWith('/workspace');
	});

	it('replaces the document for another user before the flags read answers', async () => {
		await state.readSession(serve(ada, { beta: true }));
		const { fetch, calls } = held();
		const read = state.readSession(fetch);
		// The flags request never settles. The session answer alone must change the document.
		calls.find((call) => call.url === '/api/session')?.resolve(Response.json({ user: { ...ada, id: 2 } }));
		expect(await pending(read)).toBe(true);
		expect(replace).toHaveBeenCalledExactlyOnceWith('/workspace');
		expect(state.session.changingUser).toBe(true);
		expect(state.session.data).toEqual({ user: null, flags: {} });
	});

	it('does not change the document for another user in a superseded read', async () => {
		await state.readSession(serve(ada));
		const first = held();
		const older = state.readSession(first.fetch);
		const newer = state.readSession(serve(ada));
		const snapshot = await newer;
		first.calls.find((call) => call.url === '/api/session')?.resolve(Response.json({ user: { ...ada, id: 2 } }));
		expect(await older).toBe(snapshot);
		expect(replace).not.toHaveBeenCalled();
	});
});

describe('watchSession', () => {
	it('buffers changes until it starts, then coalesces them into one read', async () => {
		let finish = () => {};
		mocks.invalidate.mockImplementation(() => new Promise<void>((resolve) => (finish = resolve)));
		channel().receive({ type: 'session-changed' });
		channel().receive({ type: 'session-changed' });
		doc.dispatchEvent(new Event('visibilitychange'));
		watch.recheckSession();
		expect(mocks.invalidate).not.toHaveBeenCalled();

		watch.watchSession();
		expect(mocks.invalidate).toHaveBeenCalledExactlyOnceWith('app:session');

		// Events during the read leave one more read for after it.
		channel().receive({ type: 'session-changed' });
		watch.recheckSession();
		expect(mocks.invalidate).toHaveBeenCalledOnce();
		finish();
		await ticks();
		expect(mocks.invalidate).toHaveBeenCalledTimes(2);
		finish();
		await ticks();
		expect(mocks.invalidate).toHaveBeenCalledTimes(2);
	});

	it('accepts only the change message, with no identity', async () => {
		watch.watchSession();
		channel().receive({ type: 'session-changed', user: ada });
		channel().receive({ type: 'signed-in' });
		channel().receive('session-changed');
		channel().receive(null);
		expect(mocks.invalidate).not.toHaveBeenCalled();
		channel().receive({ type: 'session-changed' });
		expect(mocks.invalidate).toHaveBeenCalledOnce();
	});

	it('waits for the navigation to complete', async () => {
		watch.watchSession();
		mocks.navigating.to = { url: new URL('/todos', origin) };
		watch.recheckSession();
		expect(mocks.invalidate).not.toHaveBeenCalled();
		mocks.navigating.to = null;
		watch.flushSessionCheck();
		expect(mocks.invalidate).toHaveBeenCalledOnce();
		await ticks();
		watch.flushSessionCheck();
		expect(mocks.invalidate).toHaveBeenCalledOnce();
	});

	it('checks when the tab becomes visible or the page comes back from the cache', async () => {
		watch.watchSession();
		doc.visibilityState = 'hidden';
		doc.dispatchEvent(new Event('visibilitychange'));
		win.dispatchEvent(Object.assign(new Event('pageshow'), { persisted: false }));
		expect(mocks.invalidate).not.toHaveBeenCalled();
		doc.visibilityState = 'visible';
		doc.dispatchEvent(new Event('visibilitychange'));
		expect(mocks.invalidate).toHaveBeenCalledOnce();
		await ticks();
		win.dispatchEvent(Object.assign(new Event('pageshow'), { persisted: true }));
		expect(mocks.invalidate).toHaveBeenCalledTimes(2);
	});

	it('removes the listeners and closes the channel on teardown', async () => {
		const stop = watch.watchSession();
		const opened = channel();
		stop();
		expect(opened.closed).toBe(true);
		opened.receive({ type: 'session-changed' });
		doc.dispatchEvent(new Event('visibilitychange'));
		win.dispatchEvent(Object.assign(new Event('pageshow'), { persisted: true }));
		watch.recheckSession();
		expect(mocks.invalidate).not.toHaveBeenCalled();
		// A new mount opens a new channel and handles the buffered request.
		watch.watchSession();
		expect(FakeChannel.all).toHaveLength(2);
		expect(mocks.invalidate).toHaveBeenCalledOnce();
	});

	it('announces an OIDC return with a user and clears the marker', async () => {
		await state.readSession(serve(ada));
		storage.set('spinup:oidc-pending', '1');
		watch.watchSession();
		expect(storage.has('spinup:oidc-pending')).toBe(false);
		expect(channel().sent).toEqual([{ type: 'session-changed' }]);
	});

	it('clears the OIDC marker of a failed return without an announcement', async () => {
		await state.readSession(serve(null));
		storage.set('spinup:oidc-pending', '1');
		watch.watchSession();
		expect(storage.has('spinup:oidc-pending')).toBe(false);
		expect(channel().sent).toEqual([]);
	});

	it('announces a change with no identity', () => {
		watch.announceSessionChange();
		expect(channel().sent).toEqual([{ type: 'session-changed' }]);
	});
});

describe('observedFetch', () => {
	const status = (code: number) => vi.fn(async () => new Response(null, { status: code })) as unknown as Fetch;

	it('asks for a check after a 401 of this API, and passes the response through', async () => {
		watch.watchSession();
		const res = await watch.observedFetch(status(401))('/api/todos');
		expect(res.status).toBe(401);
		expect(mocks.invalidate).toHaveBeenCalledOnce();
		await ticks();
		await watch.observedFetch(status(401))(new Request(`${origin}/api/todos/1`, { method: 'PUT' }));
		expect(mocks.invalidate).toHaveBeenCalledTimes(2);
	});

	it.each([
		['another origin', 'https://other.example.com/api/todos', 401],
		['a page path', '/healthz', 401],
		['a success', '/api/todos', 200],
		['a forbidden call', '/api/todos', 403]
	])('asks for nothing after %s', async (_name, url, code) => {
		watch.watchSession();
		await watch.observedFetch(status(code))(url);
		expect(mocks.invalidate).not.toHaveBeenCalled();
	});

	it('queues the check of a load that runs before the watch starts', async () => {
		await watch.observedFetch(status(401))('/api/todos');
		expect(mocks.invalidate).not.toHaveBeenCalled();
		watch.watchSession();
		expect(mocks.invalidate).toHaveBeenCalledOnce();
	});

	it('ignores a 401 of a request sent during a check, so a page cannot cause a loop', async () => {
		let finish = () => {};
		mocks.invalidate.mockImplementation(() => new Promise<void>((resolve) => (finish = resolve)));
		watch.watchSession();
		watch.recheckSession();
		// The check reruns the page load, which answers 401 again.
		await watch.observedFetch(status(401))('/api/reports');
		finish();
		await ticks();
		expect(mocks.invalidate).toHaveBeenCalledOnce();
	});

	it('ignores a 401 that answers a request of an older generation', async () => {
		watch.watchSession();
		const { fetch, calls } = held();
		const request = watch.observedFetch(fetch)('/api/todos');
		state.startSignOut();
		state.cancelSignOut();
		calls[0].resolve(new Response(null, { status: 401 }));
		await request;
		expect(mocks.invalidate).not.toHaveBeenCalled();
	});
});
