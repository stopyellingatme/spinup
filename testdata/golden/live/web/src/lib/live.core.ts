// Managed by spinup: `spinup upgrade` keeps this file current.

/** LiveStatus describes the connection to a feed. */
export type LiveStatus = 'connecting' | 'live' | 'reconnecting' | 'paused' | 'unauthorized';
/** Change describes one change to a row. */
export type Change<T> = { op: 'upsert'; id: string; row: T } | { op: 'delete'; id: string } | { op: 'touch'; id: string };
/** Patch contains the changes at one position in a feed. */
export type Patch<T> = { epoch: number; seq: number; changes: Change<T>[] };
/** Cursor identifies a position in a feed. */
export type Cursor = { epoch: number; seq: number };
/** LiveClientOptions supplies callbacks and replaceable sources of time and requests. */
export interface LiveClientOptions<T> {
	/** feed names the feed. */
	feed: string;
	/** fetch defaults to globalThis.fetch. */
	fetch?: typeof fetch;
	/** getCursor supplies the snapshot cursor, or null to start at the head. */
	getCursor: () => Cursor | null;
	/** onReady receives the head cursor and the resume result. */
	onReady: (cursor: Cursor, resumed: boolean) => void;
	/** onPatch receives each patch in order. */
	onPatch: (patch: Patch<T>) => void;
	/** onSync requests a snapshot. Call start after the snapshot arrives. */
	onSync: (reason: string) => void;
	/** onStatus receives changes to the connection status. */
	onStatus: (status: LiveStatus) => void;
	/** random defaults to Math.random. */
	random?: () => number;
	/** setTimer defaults to setTimeout. */
	setTimer?: typeof setTimeout;
	/** clearTimer cancels timers from setTimer. It defaults to clearTimeout. */
	clearTimer?: typeof clearTimeout;
	/** now supplies the current time in milliseconds. It defaults to Date.now. */
	now?: () => number;
}

/** parseCursor reads two decimal numbers of one to eighteen digits. */
export function parseCursor(s: string): Cursor | null {
	if (!/^[0-9]{1,18}-[0-9]{1,18}$/.test(s)) return null;
	const [epoch, seq] = s.split('-').map(Number);
	return { epoch, seq };
}

/** backoff returns a reconnect delay between 250 and 30000 milliseconds. */
export function backoff(attempt: number, random: () => number): number {
	return Math.max(250, random() * Math.min(30000, 1000 * 2 ** attempt));
}

type SSEEvent = { event: string; id: string; data: string };

/** parseSSE decodes UTF-8 and retains partial lines across chunks. */
export function parseSSE(): TransformStream<Uint8Array, SSEEvent> {
	let line = '';
	let skipLF = false;
	let event = 'message';
	let id = '';
	let data: string[] = [];
	const lines = new TransformStream<string, SSEEvent>({
		transform(chunk, controller) {
			function endLine() {
				if (line === '') {
					if (data.length) controller.enqueue({ event, id, data: data.join('\n') });
					event = 'message';
					id = '';
					data = [];
				} else if (!line.startsWith(':')) {
					const colon = line.indexOf(':');
					const field = colon < 0 ? line : line.slice(0, colon);
					let value = colon < 0 ? '' : line.slice(colon + 1);
					if (value.startsWith(' ')) value = value.slice(1);
					if (field === 'event') event = value || 'message';
					if (field === 'id' && !value.includes('\0')) id = value;
					if (field === 'data') data.push(value);
				}
				line = '';
			}
			for (const character of chunk) {
				if (skipLF && character === '\n') {
					skipLF = false;
					continue;
				}
				skipLF = character === '\r';
				if (character === '\r' || character === '\n') endLine();
				else line += character;
			}
		}
	});
	const decoder = new TextDecoderStream();
	return { writable: decoder.writable as WritableStream<Uint8Array>, readable: decoder.readable.pipeThrough(lines) };
}

/** LiveClient reads a feed with cursor resume and bounded reconnect delays. */
export class LiveClient<T> {
	private readonly options: LiveClientOptions<T>;
	private readonly request: typeof fetch;
	private readonly random: () => number;
	private readonly setTimer: typeof setTimeout;
	private readonly clearTimer: typeof clearTimeout;
	private readonly now: () => number;
	private cursor: Cursor | null = null;
	private running = false;
	private paused = false;
	private generation = 0;
	private attempt = 0;
	private controller?: AbortController;
	private reader?: ReadableStreamDefaultReader<SSEEvent>;
	private reconnectTimer?: ReturnType<typeof setTimeout>;
	private idleTimer?: ReturnType<typeof setTimeout>;

	/** Construct a client without opening a request. */
	constructor(options: LiveClientOptions<T>) {
		this.options = options;
		this.request = options.fetch ?? globalThis.fetch.bind(globalThis);
		this.random = options.random ?? Math.random;
		this.setTimer = options.setTimer ?? setTimeout.bind(globalThis);
		this.clearTimer = options.clearTimer ?? clearTimeout.bind(globalThis);
		this.now = options.now ?? Date.now;
	}

	/** start connects with the cursor from the owner. Repeated calls have no effect. */
	start(): void {
		if (this.running) return;
		this.running = true;
		this.paused = false;
		this.cursor = this.options.getCursor();
		this.attempt = 0;
		this.options.onStatus('connecting');
		if (this.running && !this.paused) void this.connect();
	}

	/** stop aborts the request and cancels all timers and callbacks. */
	stop(): void {
		this.running = false;
		this.paused = false;
		this.disconnect();
	}

	/** pause aborts the request and retains the cursor. */
	pause(): void {
		if (!this.running || this.paused) return;
		this.paused = true;
		this.disconnect();
		this.options.onStatus('paused');
	}

	/** resume reconnects a paused client after zero to 500 milliseconds. */
	resume(): void {
		if (!this.running || !this.paused) return;
		this.paused = false;
		this.schedule(this.random() * 500);
	}

	private current(generation: number): boolean {
		return this.running && !this.paused && generation === this.generation;
	}

	private disconnect(): void {
		this.generation++;
		if (this.reconnectTimer !== undefined) this.clearTimer(this.reconnectTimer);
		if (this.idleTimer !== undefined) this.clearTimer(this.idleTimer);
		this.reconnectTimer = undefined;
		this.idleTimer = undefined;
		this.controller?.abort();
		this.controller = undefined;
		void this.reader?.cancel().catch(() => {});
		this.reader = undefined;
	}

	private schedule(delay: number): void {
		this.disconnect();
		if (!this.running || this.paused) return;
		const generation = this.generation;
		this.options.onStatus('reconnecting');
		if (!this.current(generation)) return;
		this.reconnectTimer = this.setTimer(() => {
			this.reconnectTimer = undefined;
			if (this.current(generation)) void this.connect();
		}, delay);
	}

	private sync(reason: string): void {
		this.cursor = null;
		this.running = false;
		this.disconnect();
		this.options.onSync(reason);
	}

	private async connect(): Promise<void> {
		const generation = ++this.generation;
		const controller = new AbortController();
		this.controller = controller;
		let lastActivity = this.now();
		const idle = () => {
			if (!this.current(generation)) return;
			if (this.idleTimer !== undefined) this.clearTimer(this.idleTimer);
			this.idleTimer = this.setTimer(() => {
				this.idleTimer = undefined;
				if (!this.current(generation)) return;
				const remaining = 40000 - (this.now() - lastActivity);
				if (remaining > 0) idle();
				else this.schedule(backoff(this.attempt++, this.random));
			}, Math.max(0, 40000 - (this.now() - lastActivity)));
		};
		idle();
		try {
			const since = this.cursor ? `?since=${this.cursor.epoch}-${this.cursor.seq}` : '';
			const response = await this.request(`/api/live/${encodeURIComponent(this.options.feed)}/live${since}`, {
				credentials: 'same-origin', headers: { Accept: 'text/event-stream' }, signal: controller.signal
			});
			if (!this.current(generation)) {
				void response.body?.cancel().catch(() => {});
				return;
			}
			if (response.status !== 200) {
				void response.body?.cancel().catch(() => {});
				if (response.status === 401 || response.status === 403) {
					this.stop();
					const stoppedGeneration = this.generation;
					this.options.onStatus('unauthorized');
					if (response.status === 401 && stoppedGeneration === this.generation) this.options.onSync('unauthorized');
					return;
				}
				if (response.status === 400) this.cursor = null;
				let delay = backoff(this.attempt++, this.random);
				if (response.status === 503 || response.status === 429) {
					const seconds = Number(response.headers.get('Retry-After'));
					const retryAfter = Number.isFinite(seconds) ? Math.max(0, seconds) * 1000 : 0;
					delay = Math.max(retryAfter, delay) + this.random() * 1000;
				}
				this.schedule(delay);
				return;
			}
			if (!response.body) throw new Error('The live response has no body. Try: connect again.');
			const activity = new TransformStream<Uint8Array, Uint8Array>({
				transform: (chunk, output) => {
					if (this.current(generation) && chunk.byteLength) {
						lastActivity = this.now();
						idle();
					}
					output.enqueue(chunk);
				}
			});
			const reader = response.body.pipeThrough(activity).pipeThrough(parseSSE()).getReader();
			this.reader = reader;
			while (this.current(generation)) {
				const { done, value } = await reader.read();
				if (!this.current(generation)) return;
				if (done) break;
				if (!['ready', 'patch', 'sync', 'bye'].includes(value.event)) continue;
				const data = JSON.parse(value.data);
				if (value.event === 'ready') {
					const cursor = { epoch: data.epoch, seq: data.seq };
					// A resumed stream replays patches after the old cursor. Do not skip them.
					if (!data.resumed || !this.cursor) this.cursor = cursor;
					this.attempt = 0;
					this.options.onStatus('live');
					if (this.current(generation)) this.options.onReady(cursor, data.resumed === true);
				} else if (value.event === 'patch') {
					if (this.cursor && this.cursor.epoch !== 0 && this.cursor.epoch !== data.epoch) {
						this.sync('epoch');
						return;
					}
					this.cursor = { epoch: data.epoch, seq: data.seq };
					this.options.onPatch(data as Patch<T>);
				} else if (value.event === 'sync') {
					this.sync(data.reason);
					return;
				} else {
					this.schedule(data.reason === 'drain' ? 500 + this.random() * 2500 : this.random() * 1000);
					return;
				}
			}
		} catch {
			// A failed request, decoder, or reader uses the same reconnect path.
		}
		if (this.current(generation)) this.schedule(backoff(this.attempt++, this.random));
	}
}
