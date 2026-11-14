// Managed by spinup: `spinup upgrade` keeps this file current.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LiveClient, SharedFeed, backoff, parseCursor, parseSSE } from './live.core';
import type { Cursor, FeedMembership, LiveClientOptions, LiveStatus, Patch } from './live.core';

const clients: LiveClient<unknown>[] = [];
const shared: SharedFeed[] = [];
const encoder = new TextEncoder();

beforeEach(() => { vi.useFakeTimers(); });
afterEach(() => {
	for (const client of clients.splice(0)) client.stop();
	for (const feed of shared.splice(0)) feed.dispose();
	expect(vi.getTimerCount()).toBe(0);
	vi.useRealTimers();
});

async function settle() {
	await vi.advanceTimersByTimeAsync(0);
}

function stream() {
	let controller!: ReadableStreamDefaultController<Uint8Array>;
	const body = new ReadableStream<Uint8Array>({ start(value) { controller = value; } });
	return {
		response: new Response(body, { headers: { 'Content-Type': 'text/event-stream' } }),
		send: (text: string) => controller.enqueue(encoder.encode(text)),
		close: () => controller.close()
	};
}

function frame(event: string, data: unknown, id = '') {
	return `event: ${event}\nid: ${id}\ndata: ${JSON.stringify(data)}\n\n`;
}

function setup(options: Partial<LiveClientOptions<unknown>> = {}) {
	const source = stream();
	const request = vi.fn<typeof fetch>().mockResolvedValue(source.response);
	const onReady = vi.fn();
	const onPatch = vi.fn();
	const onSync = vi.fn();
	const onStatus = vi.fn<(status: LiveStatus) => void>();
	const client = new LiveClient<unknown>({
		feed: 'notes', fetch: request, getCursor: () => ({ epoch: 1, seq: 2 }),
		onReady, onPatch, onSync, onStatus, random: () => 0, ...options
	});
	clients.push(client);
	return { client, source, request, onReady, onPatch, onSync, onStatus };
}

async function parse(chunks: Uint8Array[]) {
	const input = new ReadableStream<Uint8Array>({
		start(controller) {
			for (const chunk of chunks) controller.enqueue(chunk);
			controller.close();
		}
	});
	const reader = input.pipeThrough(parseSSE()).getReader();
	const events = [];
	for (;;) {
		const result = await reader.read();
		if (result.done) return events;
		events.push(result.value);
	}
}

describe('parseSSE', () => {
	it('retains lines, UTF-8 characters, and events across byte chunks', async () => {
		const bytes = encoder.encode('event: patch\nid: 1-3\ndata: café\ndata: second\n\n');
		expect(await parse([...bytes].map((byte) => Uint8Array.of(byte)))).toEqual([
			{ event: 'patch', id: '1-3', data: 'café\nsecond' }
		]);
	});

	it('accepts CRLF, bare CR, and LF, including a split CRLF', async () => {
		expect(await parse([
			encoder.encode('event: ready\r'), encoder.encode('\ndata: one\r\n\r'),
			encoder.encode('\ndata: two\r\rdata: three\n\n')
		])).toEqual([
			{ event: 'ready', id: '', data: 'one' },
			{ event: 'message', id: '', data: 'two' },
			{ event: 'message', id: '', data: 'three' }
		]);
	});

	it('ignores comments, retry fields, and events without data', async () => {
		expect(await parse([encoder.encode(
			': ping\n\nevent: patch\nretry: 1\n\ndata:\n\n: ping\ndata: value\nretry: 2\n\n'
		)])).toEqual([
			{ event: 'message', id: '', data: '' }, { event: 'message', id: '', data: 'value' }
		]);
	});

	it('drops an unfinished event at EOF', async () => {
		expect(await parse([encoder.encode('data: incomplete\n')])).toEqual([]);
	});
});

describe('parseCursor', () => {
	it('reads decimal epoch and sequence numbers', () => {
		expect(parseCursor('1731500000000-42')).toEqual({ epoch: 1731500000000, seq: 42 });
		expect(parseCursor('0-0')).toEqual({ epoch: 0, seq: 0 });
		expect(parseCursor('001-02')).toEqual({ epoch: 1, seq: 2 });
	});

	it.each(['', '1', '-1-2', '1--2', '1-2-3', '1.5-2', '1-2\n', ' 1-2',
		'1-2 ', '1e3-2', '1234567890123456789-0'])('rejects %j', (value) => {
		expect(parseCursor(value)).toBeNull();
	});
});

describe('backoff', () => {
	it('stays between 250 and 30000 ms at both random extremes', () => {
		for (let attempt = 0; attempt <= 20; attempt++) {
			for (const random of [0, 0.999]) {
				const delay = backoff(attempt, () => random);
				expect(delay).toBeGreaterThanOrEqual(250);
				expect(delay).toBeLessThanOrEqual(30000);
			}
		}
		expect(backoff(0, () => 0.999)).toBe(999);
		expect(backoff(3, () => 0.5)).toBe(4000);
	});
});

describe('LiveClient', () => {
	it('sends the snapshot cursor and opens one request for two starts', async () => {
		const { client, request } = setup();
		client.start();
		client.start();
		await settle();
		expect(request).toHaveBeenCalledTimes(1);
		expect(request).toHaveBeenCalledWith('/api/live/notes/live?since=1-2', expect.objectContaining({
			credentials: 'same-origin', headers: { Accept: 'text/event-stream' }, signal: expect.any(AbortSignal)
		}));
	});

	it('starts at the head when there is no cursor', async () => {
		const { client, request } = setup({ getCursor: () => null });
		client.start();
		await settle();
		expect(request.mock.calls[0][0]).toBe('/api/live/notes/live');
	});

	it('reports ready and applies a patch', async () => {
		const { client, source, onReady, onPatch, onStatus } = setup();
		client.start();
		source.send(frame('ready', { epoch: 1, seq: 2, resumed: true }, '1-2'));
		source.send(frame('patch', { epoch: 1, seq: 3, changes: [] }, '1-3'));
		await settle();
		expect(onStatus).toHaveBeenLastCalledWith('live');
		expect(onReady).toHaveBeenCalledWith({ epoch: 1, seq: 2 }, true);
		expect(onPatch).toHaveBeenCalledWith({ epoch: 1, seq: 3, changes: [] });
	});

	it('drops the cursor on sync and reads the owner cursor on start', async () => {
		let cursor: Cursor | null = { epoch: 1, seq: 2 };
		const { client, source, request, onSync } = setup({ getCursor: () => cursor });
		client.start();
		source.send(frame('sync', { epoch: 1, seq: 20, reason: 'gap' }));
		await settle();
		expect(onSync).toHaveBeenCalledWith('gap');
		await vi.advanceTimersByTimeAsync(60000);
		expect(request).toHaveBeenCalledTimes(1);
		cursor = null;
		request.mockResolvedValueOnce(stream().response);
		client.start();
		await settle();
		expect(request.mock.calls[1][0]).toBe('/api/live/notes/live');
	});

	it('calls onSync for a patch with a changed epoch', async () => {
		const { client, source, onPatch, onSync } = setup();
		client.start();
		source.send(frame('patch', { epoch: 2, seq: 3, changes: [] }, '2-3'));
		await settle();
		expect(onSync).toHaveBeenCalledWith('epoch');
		expect(onPatch).not.toHaveBeenCalled();
	});

	it('accepts the first epoch after an empty snapshot', async () => {
		const { client, source, onPatch, onSync } = setup({ getCursor: () => ({ epoch: 0, seq: 0 }) });
		client.start();
		source.send(frame('patch', { epoch: 2, seq: 1, changes: [] }, '2-1'));
		await settle();
		expect(onPatch).toHaveBeenCalledOnce();
		expect(onSync).not.toHaveBeenCalled();
	});

	it.each([['age', 500], ['drain', 1750]] as const)('keeps the cursor after bye %s', async (reason, delay) => {
		const { client, source, request } = setup({ random: () => 0.5 });
		client.start();
		source.send(frame('patch', { epoch: 1, seq: 3, changes: [] }, '1-3'));
		source.send(frame('bye', { reason }));
		await settle();
		request.mockResolvedValueOnce(stream().response);
		await vi.advanceTimersByTimeAsync(delay - 1);
		expect(request).toHaveBeenCalledTimes(1);
		await vi.advanceTimersByTimeAsync(1);
		expect(request.mock.calls[1][0]).toBe('/api/live/notes/live?since=1-3');
	});

	it('does not advance a resumed cursor to the head before replay arrives', async () => {
		const { client, source, request } = setup();
		client.start();
		source.send(frame('ready', { epoch: 1, seq: 9, resumed: true }, '1-9'));
		await settle();
		client.pause();
		request.mockResolvedValueOnce(stream().response);
		client.resume();
		await settle();
		expect(request.mock.calls[1][0]).toBe('/api/live/notes/live?since=1-2');
	});

	it.each([401, 403])('stops on %s without retrying', async (status) => {
		const request = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status }));
		const { client, onStatus, onSync } = setup({ fetch: request });
		client.start();
		await settle();
		expect(onStatus).toHaveBeenLastCalledWith('unauthorized');
		if (status === 401) expect(onSync).toHaveBeenCalledExactlyOnceWith('unauthorized');
		else expect(onSync).not.toHaveBeenCalled();
		await vi.advanceTimersByTimeAsync(60000);
		expect(request).toHaveBeenCalledTimes(1);
		expect(vi.getTimerCount()).toBe(0);
	});

	it.each([429, 503])('honors Retry-After on %s', async (status) => {
		const request = vi.fn<typeof fetch>().mockResolvedValueOnce(new Response(null, {
			status, headers: { 'Retry-After': '4' }
		})).mockResolvedValue(stream().response);
		const { client } = setup({ fetch: request });
		client.start();
		await settle();
		await vi.advanceTimersByTimeAsync(3999);
		expect(request).toHaveBeenCalledTimes(1);
		await vi.advanceTimersByTimeAsync(1);
		expect(request).toHaveBeenCalledTimes(2);
	});

	it('drops a rejected cursor after a 400', async () => {
		const request = vi.fn<typeof fetch>().mockResolvedValueOnce(new Response(null, { status: 400 }))
			.mockResolvedValue(stream().response);
		const { client } = setup({ fetch: request });
		client.start();
		await settle();
		await vi.advanceTimersByTimeAsync(250);
		expect(request.mock.calls[1][0]).toBe('/api/live/notes/live');
	});

	it('backs off network failures and resets the attempt after ready', async () => {
		const source = stream();
		const request = vi.fn<typeof fetch>().mockRejectedValueOnce(new Error('offline'))
			.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(source.response)
			.mockResolvedValue(stream().response);
		const { client } = setup({ fetch: request, random: () => 0.5 });
		client.start();
		await settle();
		await vi.advanceTimersByTimeAsync(500);
		expect(request).toHaveBeenCalledTimes(2);
		await vi.advanceTimersByTimeAsync(1000);
		expect(request).toHaveBeenCalledTimes(3);
		source.send(frame('ready', { epoch: 1, seq: 2, resumed: true }, '1-2'));
		await settle();
		source.close();
		await settle();
		await vi.advanceTimersByTimeAsync(500);
		expect(request).toHaveBeenCalledTimes(4);
	});

	it('aborts after 40 seconds without bytes and reconnects', async () => {
		const { client, request } = setup();
		client.start();
		await settle();
		const signal = request.mock.calls[0][1]?.signal;
		await vi.advanceTimersByTimeAsync(39999);
		expect(signal?.aborted).toBe(false);
		request.mockResolvedValueOnce(stream().response);
		await vi.advanceTimersByTimeAsync(1);
		expect(signal?.aborted).toBe(true);
		await vi.advanceTimersByTimeAsync(250);
		expect(request).toHaveBeenCalledTimes(2);
	});

	it('counts comments and partial bytes as activity', async () => {
		const { client, source, request, onPatch } = setup();
		client.start();
		await settle();
		for (const text of [': ping\n\n', ':', ' ping']) {
			await vi.advanceTimersByTimeAsync(30000);
			source.send(text);
			await settle();
			expect(request.mock.calls[0][1]?.signal?.aborted).toBe(false);
		}
		expect(onPatch).not.toHaveBeenCalled();
	});

	it('pauses, aborts, and resumes with the retained cursor and jitter', async () => {
		const { client, source, request, onStatus } = setup({ random: () => 0.5 });
		client.start();
		source.send(frame('patch', { epoch: 1, seq: 3, changes: [] }, '1-3'));
		await settle();
		client.pause();
		expect(request.mock.calls[0][1]?.signal?.aborted).toBe(true);
		expect(onStatus).toHaveBeenLastCalledWith('paused');
		request.mockResolvedValueOnce(stream().response);
		client.resume();
		await vi.advanceTimersByTimeAsync(249);
		expect(request).toHaveBeenCalledTimes(1);
		await vi.advanceTimersByTimeAsync(1);
		expect(request.mock.calls[1][0]).toBe('/api/live/notes/live?since=1-3');
	});

	it('cancels a pending reconnect on stop', async () => {
		const request = vi.fn<typeof fetch>().mockRejectedValue(new Error('offline'));
		const { client, onStatus } = setup({ fetch: request });
		client.start();
		await settle();
		client.stop();
		const statuses = onStatus.mock.calls.length;
		await vi.advanceTimersByTimeAsync(60000);
		expect(request).toHaveBeenCalledTimes(1);
		expect(onStatus).toHaveBeenCalledTimes(statuses);
	});

	it('ignores a fetch that resolves after stop', async () => {
		let resolve!: (response: Response) => void;
		const { client, onSync, onStatus } = setup({ fetch: () => new Promise((done) => { resolve = done; }) });
		client.start();
		client.stop();
		const statuses = onStatus.mock.calls.length;
		resolve(new Response(null, { status: 401 }));
		await settle();
		expect(onStatus).toHaveBeenCalledTimes(statuses);
		expect(onSync).not.toHaveBeenCalled();
	});

	it('does not deliver buffered frames after stop inside a callback', async () => {
		const onPatch = vi.fn(() => client.stop());
		const { client, source } = setup({ onPatch });
		client.start();
		source.send(frame('patch', { epoch: 1, seq: 3, changes: [] }) + frame('patch', { epoch: 1, seq: 4, changes: [] }));
		await settle();
		expect(onPatch).toHaveBeenCalledOnce();
	});
});

// sharedSetup returns a shared feed whose requests each get a new stream.
function sharedSetup() {
	const sources: ReturnType<typeof stream>[] = [];
	const request = vi.fn<typeof fetch>().mockImplementation(async () => {
		const source = stream();
		sources.push(source);
		return source.response;
	});
	const feed = new SharedFeed({ feed: 'notes', fetch: request, random: () => 0 });
	shared.push(feed);
	return { feed, request, sources };
}

// member returns a subscriber that records the sequence of each patch.
function member(sync: () => Promise<void> = async () => {}) {
	const seqs: number[] = [];
	const subscriber = {
		status: vi.fn<(status: LiveStatus) => void>(),
		ready: vi.fn<(cursor: Cursor, resumed: boolean) => void>(),
		patch: vi.fn((patch: Patch<unknown>) => { seqs.push(patch.seq); }),
		sync: vi.fn(sync),
		catchUp: vi.fn()
	};
	return { subscriber, seqs };
}

function patches(...seqs: number[]) {
	return seqs.map((seq) => frame('patch', { epoch: 1, seq, changes: [] }, `1-${seq}`)).join('');
}

describe('SharedFeed', () => {
	it('keeps the older cursor when a subscriber with a newer snapshot joins', async () => {
		// A has the snapshot 1-0. Patch 1 commits before the stream sends it to A.
		// B loads the snapshot 1-1 and joins. A must still get patch 1.
		const { feed, request, sources } = sharedSetup();
		const a = member();
		const b = member();
		feed.join(a.subscriber, { epoch: 1, seq: 0 });
		await settle();
		sources[0].send(frame('ready', { epoch: 1, seq: 0, resumed: true }, '1-0'));
		await settle();
		feed.join(b.subscriber, { epoch: 1, seq: 1 });
		await settle();
		sources[0].send(patches(1, 2));
		await settle();
		expect(request).toHaveBeenCalledOnce();
		expect(request.mock.calls[0][0]).toBe('/api/live/notes/live?since=1-0');
		expect(a.seqs).toEqual([1, 2]);
		expect(b.seqs).toEqual([2]);
	});

	it('does not restart at a newer snapshot of one subscriber while another needs older patches', async () => {
		const { feed, request, sources } = sharedSetup();
		const a = member();
		const b = member();
		feed.join(a.subscriber, { epoch: 1, seq: 0 });
		const second: FeedMembership = feed.join(b.subscriber, { epoch: 1, seq: 0 });
		await settle();
		sources[0].send(frame('ready', { epoch: 1, seq: 0, resumed: true }, '1-0'));
		await settle();
		// B refreshes its list (a touch) and gets the snapshot 1-3.
		second.snapshot({ epoch: 1, seq: 3 });
		await settle();
		sources[0].send(patches(1, 2, 3, 4));
		await settle();
		expect(request).toHaveBeenCalledOnce();
		expect(a.seqs).toEqual([1, 2, 3, 4]);
		expect(b.seqs).toEqual([4]);
	});

	it('replays from an older snapshot and skips the patches that a subscriber has', async () => {
		const { feed, request, sources } = sharedSetup();
		const a = member();
		const b = member();
		feed.join(a.subscriber, { epoch: 1, seq: 0 });
		await settle();
		sources[0].send(frame('ready', { epoch: 1, seq: 0, resumed: true }, '1-0') + patches(1, 2));
		await settle();
		// B loaded the snapshot 1-1 before the stream sent patch 2.
		feed.join(b.subscriber, { epoch: 1, seq: 1 });
		await settle();
		expect(request).toHaveBeenCalledTimes(2);
		expect(request.mock.calls[1][0]).toBe('/api/live/notes/live?since=1-1');
		sources[1].send(frame('ready', { epoch: 1, seq: 2, resumed: true }, '1-2') + patches(2, 3));
		await settle();
		expect(a.seqs).toEqual([1, 2, 3]);
		expect(b.seqs).toEqual([2, 3]);
	});

	it('restarts at the oldest new snapshot after a sync', async () => {
		const { feed, request, sources } = sharedSetup();
		let first!: FeedMembership;
		let second!: FeedMembership;
		const a = member(async () => { first.snapshot({ epoch: 1, seq: 9 }); });
		const b = member(async () => { second.snapshot({ epoch: 1, seq: 7 }); });
		first = feed.join(a.subscriber, { epoch: 1, seq: 0 });
		second = feed.join(b.subscriber, { epoch: 1, seq: 0 });
		await settle();
		sources[0].send(frame('sync', { epoch: 1, seq: 9, reason: 'gap' }));
		await settle();
		expect(a.subscriber.sync).toHaveBeenCalledOnce();
		expect(b.subscriber.sync).toHaveBeenCalledOnce();
		expect(request).toHaveBeenCalledTimes(2);
		expect(request.mock.calls[1][0]).toBe('/api/live/notes/live?since=1-7');
		sources[1].send(frame('ready', { epoch: 1, seq: 9, resumed: true }, '1-9') + patches(8, 9, 10));
		await settle();
		expect(a.seqs).toEqual([10]);
		expect(b.seqs).toEqual([8, 9, 10]);
	});

	it('refreshes a liveRefresh subscriber that joins a feed that is already live', async () => {
		const { feed, request, sources } = sharedSetup();
		const first = member();
		const second = member();
		feed.join(first.subscriber, null);
		await settle();
		expect(request.mock.calls[0][0]).toBe('/api/live/notes/live');
		sources[0].send(frame('ready', { epoch: 1, seq: 5, resumed: false }, '1-5'));
		await settle();
		expect(first.subscriber.ready).toHaveBeenCalledExactlyOnceWith({ epoch: 1, seq: 5 }, false);
		// The page of the second subscriber loaded before a patch that the stream already sent.
		feed.join(second.subscriber, null);
		expect(second.subscriber.catchUp).toHaveBeenCalledOnce();
		expect(first.subscriber.catchUp).not.toHaveBeenCalled();
		await settle();
		expect(request).toHaveBeenCalledOnce();
		expect(second.subscriber.status).toHaveBeenCalledWith('live');
	});

	it('refreshes a liveRefresh subscriber that joins a stream that resumes from a cursor', async () => {
		const { feed, request, sources } = sharedSetup();
		const list = member();
		const page = member();
		feed.join(list.subscriber, { epoch: 1, seq: 3 });
		await settle();
		sources[0].send(frame('ready', { epoch: 1, seq: 3, resumed: true }, '1-3'));
		await settle();
		feed.join(page.subscriber, null);
		expect(page.subscriber.catchUp).toHaveBeenCalledOnce();
		sources[0].send(patches(4));
		await settle();
		expect(request).toHaveBeenCalledOnce();
		expect(page.seqs).toEqual([4]);
		expect(list.seqs).toEqual([4]);
	});

	it('does not refresh twice when a subscriber joins before a ready at the head', async () => {
		const { feed, sources } = sharedSetup();
		const first = member();
		const second = member();
		feed.join(first.subscriber, null);
		feed.join(second.subscriber, null);
		await settle();
		expect(second.subscriber.catchUp).not.toHaveBeenCalled();
		sources[0].send(frame('ready', { epoch: 1, seq: 5, resumed: false }, '1-5'));
		await settle();
		expect(second.subscriber.ready).toHaveBeenCalledExactlyOnceWith({ epoch: 1, seq: 5 }, false);
	});

	it('stops the stream when the last subscriber leaves', async () => {
		const { feed, request } = sharedSetup();
		const a = member();
		const b = member();
		const first = feed.join(a.subscriber, null);
		const second = feed.join(b.subscriber, null);
		await settle();
		const signal = request.mock.calls[0][1]?.signal;
		expect(first.release()).toBe(false);
		expect(signal?.aborted).toBe(false);
		expect(second.release()).toBe(true);
		expect(signal?.aborted).toBe(true);
	});
});
