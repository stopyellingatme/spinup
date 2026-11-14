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

/** FeedSubscriber receives the events of a shared feed. */
export interface FeedSubscriber {
	/** status receives each change of the connection status. */
	status(status: LiveStatus): void;
	/** ready receives the ready event of each stream. */
	ready(cursor: Cursor, resumed: boolean): void;
	/** patch receives each patch after the cursor of the subscriber, in order. */
	patch(patch: Patch<unknown>): void;
	/** sync loads the snapshot again. A subscriber with a cursor then calls snapshot. */
	sync(): Promise<void>;
	/** catchUp runs once when a subscriber with no cursor joins a feed that is already open. */
	catchUp?(): void;
}

/** FeedMembership is the link of one subscriber to a shared feed. */
export interface FeedMembership {
	/** snapshot sets the cursor of a new snapshot of the subscriber. */
	snapshot(cursor: Cursor): void;
	/** release removes the subscriber. It returns true when the feed has no subscriber left. */
	release(): boolean;
}

/** SharedFeedOptions configures a SharedFeed. */
export interface SharedFeedOptions {
	/** feed names the feed. */
	feed: string;
	/** fetch goes to the LiveClient. It defaults to globalThis.fetch. */
	fetch?: typeof fetch;
	/** random goes to the LiveClient. It defaults to Math.random. */
	random?: () => number;
	/** setTimer goes to the LiveClient. It defaults to setTimeout. */
	setTimer?: typeof setTimeout;
	/** clearTimer goes to the LiveClient. It defaults to clearTimeout. */
	clearTimer?: typeof clearTimeout;
	/** now goes to the LiveClient. It defaults to Date.now. */
	now?: () => number;
	/** blocked reports a hidden or offline tab. The feed then does not connect. */
	blocked?: () => boolean;
	/** onUnauthorized runs once after the first 401 answer. */
	onUnauthorized?: () => void;
}

type Member = { subscriber: FeedSubscriber; position: Cursor | null };

// older reports whether the cursor a is before the cursor b. The cursor 0-0 of
// an empty snapshot is before every other cursor. Cursors of two other epochs
// do not compare.
function older(a: Cursor, b: Cursor): boolean {
	if (a.epoch === b.epoch) return a.seq < b.seq;
	return a.epoch === 0;
}

/**
 * SharedFeed shares one stream of a feed between the subscribers of a tab.
 * Each subscriber with a cursor keeps its own position. The stream starts at
 * the oldest position that a subscriber still needs, and each subscriber gets
 * only the patches after its own position. A subscriber with a newer snapshot
 * thus never makes another subscriber skip a patch.
 */
export class SharedFeed {
	/** status is the current connection status. */
	status: LiveStatus = 'connecting';
	private readonly client: LiveClient<unknown>;
	private readonly blocked: () => boolean;
	private readonly onUnauthorized: () => void;
	private readonly members = new Set<Member>();
	// stream is the position of the stream: the start cursor, then the last patch.
	private stream: Cursor | null = null;
	private start: Cursor | null = null;
	private running = false;
	private readySeen = false;
	private syncing = false;
	private disposed = false;
	private unauthorizedReported = false;

	/** Construct a feed without opening a request. The first join starts it. */
	constructor(options: SharedFeedOptions) {
		this.blocked = options.blocked ?? (() => false);
		this.onUnauthorized = options.onUnauthorized ?? (() => {});
		this.client = new LiveClient<unknown>({
			feed: options.feed, fetch: options.fetch, random: options.random,
			setTimer: options.setTimer, clearTimer: options.clearTimer, now: options.now,
			getCursor: () => this.start,
			onStatus: (status) => this.setStatus(status),
			onReady: (cursor, resumed) => this.ready(cursor, resumed),
			onPatch: (patch) => this.patch(patch),
			onSync: (reason) => this.sync(reason)
		});
	}

	/** join adds a subscriber. cursor is the cursor of its snapshot, or null. */
	join(subscriber: FeedSubscriber, cursor: Cursor | null): FeedMembership {
		const member: Member = { subscriber, position: cursor };
		const fresh = this.members.size === 0 && !this.running;
		this.members.add(member);
		subscriber.status(this.status);
		if (fresh) this.restart();
		else if (cursor) this.place(member, cursor);
		else if (this.readySeen || this.start !== null) {
			// The stream does not send a ready with resumed false that would refresh it.
			subscriber.catchUp?.();
		}
		return {
			snapshot: (next) => {
				if (!this.members.has(member)) return;
				member.position = next;
				this.place(member, next);
			},
			release: () => {
				if (!this.members.delete(member)) return this.members.size === 0;
				if (this.members.size === 0) this.dispose();
				return this.members.size === 0;
			}
		};
	}

	/** pause aborts the stream and keeps its cursor. */
	pause(): void {
		this.client.pause();
	}

	/** resume connects again after a pause, or starts a stream that a blocked tab deferred. */
	resume(): void {
		if (this.blocked() || this.disposed || this.syncing || this.status === 'unauthorized') return;
		this.client.resume();
		if (this.status === 'paused') this.restart();
	}

	/** dispose stops the stream. The feed does nothing after it. */
	dispose(): void {
		this.disposed = true;
		this.running = false;
		this.client.stop();
	}

	// place decides whether the stream must start again for a new snapshot cursor.
	private place(member: Member, cursor: Cursor): void {
		if (this.syncing) return;
		const stream = this.stream;
		if (!this.running || stream === null || older(cursor, stream)) {
			this.restart();
			return;
		}
		if (cursor.epoch !== stream.epoch && stream.epoch !== 0) {
			if (cursor.epoch > stream.epoch) this.restart();
			// A snapshot of an older epoch is stale: load it again.
			else this.resync(member);
		}
		// Otherwise the stream is at or before the cursor. The member skips the patches it has.
	}

	// required returns the oldest position that a member still needs, or null.
	private required(): Cursor | null {
		let newest = 0;
		for (const member of this.members) {
			if (member.position) newest = Math.max(newest, member.position.epoch);
		}
		let oldest: Cursor | null = null;
		for (const member of this.members) {
			const position = member.position;
			if (!position) continue;
			if (position.epoch !== 0 && position.epoch < newest) {
				this.resync(member);
				continue;
			}
			if (!oldest || older(position, oldest)) oldest = position;
		}
		return oldest;
	}

	private restart(): void {
		if (this.disposed || this.syncing || this.status === 'unauthorized') return;
		this.client.stop();
		this.running = false;
		// A snapshot can complete while the tab is offline or hidden.
		if (this.blocked()) {
			this.setStatus('paused');
			return;
		}
		this.start = this.required();
		this.stream = this.start;
		this.readySeen = false;
		this.running = true;
		this.client.start();
	}

	private resync(member: Member): void {
		member.position = null;
		void member.subscriber.sync().catch(() => {});
	}

	private setStatus(status: LiveStatus): void {
		this.status = status;
		for (const member of [...this.members]) member.subscriber.status(status);
	}

	private ready(cursor: Cursor, resumed: boolean): void {
		this.readySeen = true;
		if (!resumed) {
			this.stream = cursor;
			// The stream starts at the head. A member before it missed patches.
			for (const member of [...this.members]) {
				if (member.position && older(member.position, cursor)) this.resync(member);
			}
		}
		for (const member of [...this.members]) member.subscriber.ready(cursor, resumed);
	}

	private patch(patch: Patch<unknown>): void {
		const at = { epoch: patch.epoch, seq: patch.seq };
		this.stream = at;
		for (const member of [...this.members]) {
			const position = member.position;
			if (position && position.epoch !== 0 && position.epoch !== at.epoch) {
				this.resync(member);
				continue;
			}
			if (position && position.epoch === at.epoch && position.seq >= at.seq) continue;
			if (position) member.position = at;
			member.subscriber.patch(patch);
		}
	}

	private sync(reason: string): void {
		if (reason === 'unauthorized') {
			if (!this.unauthorizedReported) {
				this.unauthorizedReported = true;
				this.onUnauthorized();
			}
			return;
		}
		this.running = false;
		this.stream = null;
		this.syncing = true;
		const members = [...this.members];
		for (const member of members) member.position = null;
		void Promise.all(members.map((member) => member.subscriber.sync())).then(() => {
			this.syncing = false;
			if (!this.disposed) this.restart();
		}, () => { this.syncing = false; });
	}
}
