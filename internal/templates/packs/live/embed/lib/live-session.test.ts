// Managed by spinup: `spinup upgrade` keeps this file current.
// The tests of the session pause of lib/live.svelte.ts (ADR 0039). They use the real signOut() of
// lib/sign-out.ts. One list records each request, each abort, the announcement, and the document
// change in order, so the order comes from the events and not from a timer.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { LiveStatus } from './live.core';

const mocks = vi.hoisted(() => ({
	invalidate: vi.fn(),
	invalidateAll: vi.fn(),
	navigating: { to: null as unknown },
	toast: vi.fn()
}));
vi.mock('$app/navigation', () => ({ invalidate: mocks.invalidate, invalidateAll: mocks.invalidateAll }));
vi.mock('$app/state', () => ({ navigating: mocks.navigating }));
vi.mock('svelte-sonner', () => ({ toast: { error: mocks.toast } }));
vi.mock('#lib/nav.gen.js', () => ({ home: '/workspace' }));

type Fetch = typeof globalThis.fetch;
type Row = { id: number; title: string };
type Held = { path: string; give: (res: Response) => void };
const ada = { id: 1, email: 'ada@example.com', role: 'staff' };
const origin = 'https://app.example.com';
const stream = '/api/live/todos/live';
const snapshot = '/api/live/todos';

let log: string[];
let answers: (() => Promise<Response>)[];
let held: Held[];
let rejectOnAbort: boolean;
let state: typeof import('./session.svelte.js');
let watch: typeof import('./session-watch.js');
let live: typeof import('./live.svelte.js');
let signOut: typeof import('./sign-out.js').signOut;

beforeEach(async () => {
	vi.resetModules();
	log = [];
	answers = [];
	held = [];
	rejectOnAbort = true;
	mocks.invalidate.mockReset();
	mocks.invalidate.mockResolvedValue(undefined);
	mocks.invalidateAll.mockReset();
	mocks.invalidateAll.mockResolvedValue(undefined);
	mocks.toast.mockReset();
	mocks.navigating.to = null;
	const location = { origin, replace: (target: string) => log.push(`replace ${target}`) };
	vi.stubGlobal('location', location);
	vi.stubGlobal('window', Object.assign(new EventTarget(), { location }));
	vi.stubGlobal('document', Object.assign(new EventTarget(), { visibilityState: 'visible' }));
	vi.stubGlobal('navigator', { onLine: true });
	vi.stubGlobal('sessionStorage', { getItem: () => null, setItem: () => {}, removeItem: () => {} });
	vi.stubGlobal(
		'BroadcastChannel',
		class {
			onmessage: ((event: MessageEvent) => void) | null = null;
			postMessage(data: unknown) {
				log.push(`announce ${JSON.stringify(data)}`);
			}
			close() {}
		}
	);
	// The logout request gets the next queued answer, or 204. A live request waits for the test.
	// An abort rejects it, as in a browser, unless the test sends a late answer.
	vi.stubGlobal(
		'fetch',
		vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
			const path = String(input);
			log.push(`${init?.method ?? 'GET'} ${path}`);
			if (path === '/api/auth/logout') {
				return (answers.shift() ?? (async () => new Response(null, { status: 204 })))();
			}
			return new Promise<Response>((resolve, reject) => {
				held.push({ path, give: resolve });
				init?.signal?.addEventListener('abort', () => {
					log.push(`abort ${path}`);
					if (rejectOnAbort) reject(new DOMException('The request was aborted.', 'AbortError'));
				});
			});
		})
	);
	vi.spyOn(console, 'warn').mockImplementation(() => {});
	vi.spyOn(console, 'error').mockImplementation(() => {});
	state = await import('./session.svelte.js');
	watch = await import('./session-watch.js');
	live = await import('./live.svelte.js');
	({ signOut } = await import('./sign-out.js'));
	await state.readSession(sessionOf(ada));
	watch.watchSession();
	log = [];
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

const sessionOf = (user: App.User | null): Fetch =>
	(async (input: RequestInfo | URL) =>
		String(input) === '/api/session'
			? user
				? Response.json({ user })
				: new Response(null, { status: 401 })
			: Response.json({ flags: {} })) as unknown as Fetch;

async function ticks(n = 50) {
	for (let i = 0; i < n; i++) await Promise.resolve();
}

/** hold queues a logout answer that the test gives later. */
function hold(): (res: Response) => void {
	let give: (res: Response) => void = () => {};
	answers.push(() => new Promise<Response>((resolve) => (give = resolve)));
	return (res) => give(res);
}

/** answer gives the oldest waiting request of path its response. */
function answer(path: string, res: Response): void {
	const index = held.findIndex((request) => request.path === path);
	if (index < 0) throw new Error(`no request waits for ${path}`);
	held.splice(index, 1)[0].give(res);
}

function rows(epoch: number, seq: number, list: Row[]): Response {
	return Response.json({ epoch, seq, rows: list });
}

/** ready answers a stream request with a ready event that does not resume. */
function ready(epoch: number, seq: number): Response {
	const body = new ReadableStream<Uint8Array>({
		start(controller) {
			const text = `event: ready\ndata: ${JSON.stringify({ epoch, seq, resumed: false })}\n\n`;
			controller.enqueue(new TextEncoder().encode(text));
		}
	});
	return new Response(body, { status: 200, headers: { 'content-type': 'text/event-stream' } });
}

function list(feed = 'todos') {
	const view = { rows: [] as Row[], status: 'connecting' as LiveStatus, more: false };
	return { view, ...live.subscribeList<Row>(feed, {}, view) };
}

function refresher(options: Parameters<typeof live.subscribeRefresh>[1] = {}) {
	const seen: LiveStatus[] = [];
	const close = live.subscribeRefresh('todos', options, (value) => seen.push(value));
	return { seen, close };
}

const sessionChecks = () => mocks.invalidate.mock.calls.filter(([key]) => key === 'app:session').length;

describe('the live helper during sign-out', () => {
	it('aborts the stream and the snapshot before the logout request, with one stream per feed', async () => {
		const first = refresher();
		const second = refresher();
		const todos = list();
		expect(log).toEqual([`GET ${stream}`, `GET ${snapshot}`]);
		const done = signOut();
		// Each stop runs before signOut returns: no await comes before the logout request.
		expect(log).toEqual([`GET ${stream}`, `GET ${snapshot}`, `abort ${stream}`, `abort ${snapshot}`, 'POST /api/auth/logout']);
		expect(todos.view.status).toBe('paused');
		expect(first.seen.at(-1)).toBe('paused');
		expect(second.seen.at(-1)).toBe('paused');
		expect(await done).toBe(true);
		expect(log.slice(5)).toEqual(['announce {"type":"session-changed"}', 'replace /login']);
		expect(mocks.invalidateAll).not.toHaveBeenCalled();
		expect(sessionChecks()).toBe(0);
	});

	it('starts a subscription paused during the sign-out, and starts it when the logout fails', async () => {
		const give = hold();
		const done = signOut();
		const before = state.session.generation;
		const todos = list();
		const page = refresher();
		expect(todos.view.status).toBe('paused');
		expect(page.seen).toEqual(['paused']);
		// No visibility, network, or refresh event opens a stream during the pause.
		window.dispatchEvent(new Event('online'));
		document.dispatchEvent(new Event('visibilitychange'));
		await todos.refresh();
		expect(log).toEqual(['POST /api/auth/logout']);

		give(new Response(null, { status: 500 }));
		expect(await done).toBe(false);
		expect(mocks.toast).toHaveBeenCalledWith('Sign-out failed. Something went wrong on the server. Try again later.');
		expect(state.session.generation).toBe(before);
		expect(log).toEqual(['POST /api/auth/logout', `GET ${snapshot}`, `GET ${stream}`]);
		answer(snapshot, rows(1, 4, [{ id: 1, title: 'Write the report' }]));
		await ticks();
		expect(todos.view.rows).toEqual([{ id: 1, title: 'Write the report' }]);
		expect(todos.view.status).not.toBe('paused');
	});

	it('ignores late answers and timers after the stop', async () => {
		vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
		rejectOnAbort = false;
		const page = refresher();
		answer(stream, ready(1, 0));
		await ticks();
		// The ready event schedules a page refresh after the debounce delay.
		expect(page.seen).toContain('live');
		const todos = list();
		const reports = list('reports');
		const give = hold();
		const done = signOut();
		const stopped = [
			`GET ${stream}`,
			`GET ${snapshot}`,
			'GET /api/live/reports',
			`abort ${stream}`,
			`abort ${snapshot}`,
			'abort /api/live/reports',
			'POST /api/auth/logout'
		];
		expect(log).toEqual(stopped);

		// The snapshots answer after the stop, one with rows and one with a 401. The debounce
		// delay passes.
		answer(snapshot, rows(1, 2, [{ id: 7, title: 'Too late' }]));
		answer('/api/live/reports', new Response(null, { status: 401 }));
		await ticks();
		vi.advanceTimersByTime(5000);
		await ticks();
		expect(todos.view.rows).toEqual([]);
		expect(todos.view.status).toBe('paused');
		expect(reports.view.status).toBe('paused');
		expect(mocks.invalidate).not.toHaveBeenCalled();
		expect(mocks.invalidateAll).not.toHaveBeenCalled();
		expect(log).toEqual(stopped);

		give(new Response(null, { status: 503 }));
		expect(await done).toBe(false);
		// The late answers asked for no session check, also after the pause ended.
		expect(sessionChecks()).toBe(0);
		expect(log.slice(stopped.length)).toEqual([`GET ${stream}`, `GET ${snapshot}`, 'GET /api/live/reports']);
	});

	it('resumes the snapshots after a failed logout, and a second attempt succeeds', async () => {
		const todos = list();
		answer(snapshot, rows(1, 1, [{ id: 1, title: 'Old' }]));
		await ticks();
		expect(log).toEqual([`GET ${snapshot}`, `GET ${stream}?since=1-1`]);

		answers.push(async () => new Response(null, { status: 503 }));
		expect(await signOut()).toBe(false);
		expect(log.slice(2)).toEqual([`abort ${stream}?since=1-1`, 'POST /api/auth/logout', `GET ${snapshot}`]);
		answer(snapshot, rows(1, 3, [{ id: 1, title: 'Old' }, { id: 2, title: 'New' }]));
		await ticks();
		expect(todos.view.rows.map((row) => row.title)).toEqual(['Old', 'New']);
		expect(log.slice(5)).toEqual([`GET ${stream}?since=1-3`]);

		const done = signOut();
		expect(log.slice(6)).toEqual([`abort ${stream}?since=1-3`, 'POST /api/auth/logout']);
		expect(await done).toBe(true);
		expect(log.slice(8)).toEqual(['announce {"type":"session-changed"}', 'replace /login']);
		expect(mocks.invalidateAll).not.toHaveBeenCalled();
	});
});

describe('the live helper and 401 answers', () => {
	it('asks for a session check on a 401 snapshot, and never invalidates all page data', async () => {
		const todos = list();
		answer(snapshot, new Response(null, { status: 401 }));
		await ticks();
		expect(todos.view.status).toBe('unauthorized');
		expect(sessionChecks()).toBe(1);
		expect(mocks.invalidateAll).not.toHaveBeenCalled();
	});

	it('asks for a session check on a 401 stream', async () => {
		const page = refresher();
		answer(stream, new Response(null, { status: 401 }));
		await ticks();
		expect(page.seen.at(-1)).toBe('unauthorized');
		expect(sessionChecks()).toBe(1);
		expect(mocks.invalidateAll).not.toHaveBeenCalled();
	});

	it('invalidates only the dependency key of liveRefresh', async () => {
		vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
		const page = refresher({ invalidate: 'app:resource:todos' });
		answer(stream, ready(1, 0));
		await ticks();
		vi.advanceTimersByTime(200);
		await ticks();
		expect(mocks.invalidate).toHaveBeenCalledWith('app:resource:todos');
		expect(mocks.invalidateAll).not.toHaveBeenCalled();
		page.close();
	});
});
