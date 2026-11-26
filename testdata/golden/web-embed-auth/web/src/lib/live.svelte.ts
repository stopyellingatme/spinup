// Managed by spinup: `spinup upgrade` keeps this file current.
// The live helper of embed mode (ADR 0033, ADR 0039). It has the API of node mode, and it follows
// the session: a sign-out stops each stream, snapshot request, and timer of this tab before the
// logout request. A failed sign-out starts them again. A 401 asks the session watch for a new read.
import { invalidate, invalidateAll } from '$app/navigation';
import { untrack } from 'svelte';
import { recheckSession } from '#lib/session-watch.js';
import { onSessionPause, session } from '#lib/session.svelte.js';
import { SharedFeed, backoff } from './live.core';
import type { Cursor, FeedSubscriber, LiveStatus, Patch } from './live.core';

// The Go binary serves the embed app, so this code runs in a browser. The check keeps the module
// safe to import in a test or a prerender.
const browser = typeof window !== 'undefined';

/** LiveRefreshOptions controls the delay and target of page refreshes. */
export interface LiveRefreshOptions {
	/** debounceMs is the trailing delay in milliseconds. The default is 200. */
	debounceMs?: number;
	/** maxWaitMs limits the delay from the first patch. The default is 2000. */
	maxWaitMs?: number;
	/** invalidate selects a dependency or a function. The default refreshes all page data. */
	invalidate?: string | (() => unknown);
}
/** LiveState exposes the connection status. */
export interface LiveState {
	/** status is the current connection status. */
	readonly status: LiveStatus;
}
/** LiveListOptions controls the initial rows, order, and size of a list. */
export interface LiveListOptions<T extends { id: number | string }> {
	/** initial supplies rows immediately. A background snapshot supplies the cursor. */
	initial?: T[];
	/** sort orders rows after an upsert. The default retains the server order. */
	sort?: (a: T, b: T) => number;
	/** limit caps the rows after an upsert. The default is 500. */
	limit?: number;
}
/** LiveList exposes rows from a snapshot and subsequent patches. */
export interface LiveList<T> {
	/** rows contains the current rows. */
	readonly rows: T[];
	/** status is the current connection status. */
	readonly status: LiveStatus;
	/** more indicates that the snapshot exceeded its limit. */
	readonly more: boolean;
	/** refresh replaces the rows with a new snapshot. */
	refresh(): Promise<void>;
}
/** LiveListView is the state that subscribeList writes. liveList keeps it in $state. */
export interface LiveListView<T> {
	rows: T[];
	status: LiveStatus;
	more: boolean;
}

type Feed = { shared: SharedFeed; dispose: () => void };
const feeds = new Map<string, Feed>();

// paused reports a sign-out or a departure. Nothing opens a stream or asks for a snapshot then.
function paused(): boolean {
	return session.signingOut || session.changingUser;
}

// unauthorized handles a 401 of a stream or a snapshot. An answer of an older generation, or one
// that arrives during a sign-out, belongs to the old session: it asks for nothing. Otherwise the
// session watch reads the session again, and the root layout decides what the user sees.
function unauthorized(started: number): void {
	if (started !== session.generation || paused()) return;
	recheckSession();
}

// createFeed connects a shared feed to the visibility and network events of the tab.
function createFeed(name: string): Feed {
	const started = session.generation;
	let hiddenTimer: ReturnType<typeof setTimeout> | undefined;
	let hiddenPaused = false;
	let offline = !navigator.onLine;
	const shared = new SharedFeed({
		feed: name,
		blocked: () => hiddenPaused || offline || paused(),
		onUnauthorized: () => unauthorized(started)
	});
	function visibility() {
		clearTimeout(hiddenTimer);
		hiddenTimer = undefined;
		if (document.visibilityState === 'hidden') {
			hiddenTimer = setTimeout(() => {
				hiddenTimer = undefined;
				if (document.visibilityState === 'hidden') {
					hiddenPaused = true;
					shared.pause();
				}
			}, 60000);
		} else {
			hiddenPaused = false;
			shared.resume();
		}
	}
	function goOffline() {
		offline = true;
		shared.pause();
	}
	function goOnline() {
		offline = false;
		shared.resume();
	}
	document.addEventListener('visibilitychange', visibility);
	window.addEventListener('offline', goOffline);
	window.addEventListener('online', goOnline);
	visibility();
	return {
		shared,
		dispose() {
			shared.dispose();
			clearTimeout(hiddenTimer);
			document.removeEventListener('visibilitychange', visibility);
			window.removeEventListener('offline', goOffline);
			window.removeEventListener('online', goOnline);
		}
	};
}

// join adds a subscriber to the one stream of the feed in this tab. The last release stops the
// stream and removes the listeners of the feed.
function join(feed: string, subscriber: FeedSubscriber, cursor: Cursor | null) {
	let entry = feeds.get(feed);
	if (!entry) {
		entry = createFeed(feed);
		feeds.set(feed, entry);
	}
	const joined = entry;
	const membership = joined.shared.join(subscriber, cursor);
	return {
		snapshot: (next: Cursor) => membership.snapshot(next),
		release() {
			if (membership.release() && feeds.get(feed) === joined) {
				joined.dispose();
				feeds.delete(feed);
			}
		}
	};
}

// follow registers the stop and the resume of one subscription with the session. During a
// sign-out the registration stops the subscription at once, so it starts paused. It returns the
// function that removes the registration.
function follow(start: () => void, stop: () => void): () => void {
	let stopped = false;
	const unregister = onSessionPause(
		() => {
			stopped = true;
			stop();
		},
		() => {
			stopped = false;
			start();
		}
	);
	if (!stopped) start();
	return unregister;
}

/**
 * subscribeRefresh is liveRefresh without runes: it subscribes to the feed, refreshes page data on
 * patches, and follows the session. It returns the function that ends the subscription. Pages call
 * liveRefresh. Tests call this function.
 */
export function subscribeRefresh(
	feed: string,
	options: LiveRefreshOptions,
	setStatus: (status: LiveStatus) => void
): () => void {
	// run advances on each stop. A callback or a timer of an older run does nothing.
	let run = 0;
	let trailing: ReturnType<typeof setTimeout> | undefined;
	let maximum: ReturnType<typeof setTimeout> | undefined;
	let subscription: ReturnType<typeof join> | undefined;
	function clear() {
		clearTimeout(trailing);
		clearTimeout(maximum);
		trailing = maximum = undefined;
	}
	async function refresh(at: number) {
		clear();
		if (at !== run) return;
		if (typeof options.invalidate === 'function') await options.invalidate();
		else if (typeof options.invalidate === 'string') await invalidate(options.invalidate);
		else await invalidateAll();
	}
	function schedule(at: number) {
		if (at !== run) return;
		clearTimeout(trailing);
		const fire = () => {
			void refresh(at).catch(() => {});
		};
		trailing = setTimeout(fire, options.debounceMs ?? 200);
		maximum ??= setTimeout(fire, options.maxWaitMs ?? 2000);
	}
	function start() {
		const at = run;
		setStatus('connecting');
		subscription = join(
			feed,
			{
				status: (value) => {
					if (at !== run) return;
					setStatus(value);
					if (value === 'unauthorized') clear();
				},
				ready: (_cursor, resumed) => {
					if (!resumed) schedule(at);
				},
				patch: () => schedule(at),
				sync: () => refresh(at),
				// The page may have loaded before a patch that the open stream already sent.
				catchUp: () => schedule(at)
			},
			null
		);
	}
	function stop() {
		run++;
		clear();
		const current = subscription;
		subscription = undefined;
		current?.release();
	}
	const unregister = follow(start, () => {
		stop();
		setStatus('paused');
	});
	return () => {
		unregister();
		stop();
	};
}

/**
 * subscribeList is liveList without runes: it keeps view current from snapshots and patches, and
 * follows the session. Pages call liveList. Tests call this function.
 */
export function subscribeList<T extends { id: number | string }>(
	feed: string,
	options: LiveListOptions<T>,
	view: LiveListView<T>
): { refresh(): Promise<void>; close(): void } {
	// run advances on each stop. An answer, a callback, or a timer of an older run does nothing.
	let run = 0;
	let stopped = false;
	let closed = false;
	let started = session.generation;
	let attempt = 0;
	let retry: ReturnType<typeof setTimeout> | undefined;
	let controller: AbortController | undefined;
	let pending: Promise<void> | undefined;
	let subscription: ReturnType<typeof join> | undefined;
	let unauthorizedChecked = false;
	function apply(at: number, patch: Patch<unknown>) {
		if (at !== run) return;
		for (const change of patch.changes) {
			if (change.op === 'touch') {
				void refresh().catch(() => {});
				continue;
			}
			if (change.op === 'delete') view.rows = view.rows.filter((row) => String(row.id) !== change.id);
			else {
				const row = change.row as T;
				const index = view.rows.findIndex((value) => String(value.id) === String(row.id));
				if (index < 0) view.rows.push(row);
				else view.rows[index] = row;
				if (options.sort) view.rows.sort(options.sort);
				view.rows = view.rows.slice(0, options.limit ?? 500);
			}
		}
	}
	async function snapshot(at: number) {
		const own = new AbortController();
		controller = own;
		try {
			const response = await fetch(`/api/live/${encodeURIComponent(feed)}`, {
				credentials: 'same-origin',
				signal: own.signal
			});
			if (at !== run) {
				void response.body?.cancel().catch(() => {});
				return;
			}
			if (response.status === 401 || response.status === 403) {
				view.status = 'unauthorized';
				subscription?.release();
				subscription = undefined;
				if (response.status === 401 && !unauthorizedChecked) {
					unauthorizedChecked = true;
					unauthorized(started);
				}
				return;
			}
			if (!response.ok) throw new Error('The live snapshot failed. Try: refresh the list.');
			const data: { epoch: number; seq: number; rows: T[]; more?: boolean } = await response.json();
			if (at !== run) return;
			view.rows = data.rows;
			view.more = data.more === true;
			attempt = 0;
			const cursor = { epoch: data.epoch, seq: data.seq };
			if (subscription) subscription.snapshot(cursor);
			else
				subscription = join(
					feed,
					{
						status: (value) => {
							if (at !== run) return;
							view.status = value;
							if (value === 'unauthorized') {
								clearTimeout(retry);
								retry = undefined;
								controller?.abort();
							}
						},
						ready: () => {},
						patch: (next) => apply(at, next),
						sync: refresh
					},
					cursor
				);
		} catch (error) {
			if (at !== run || view.status === 'unauthorized') return;
			view.status = 'reconnecting';
			retry = setTimeout(() => {
				retry = undefined;
				if (at === run) void refresh().catch(() => {});
			}, backoff(attempt++, Math.random));
			throw error;
		} finally {
			if (controller === own) controller = undefined;
		}
	}
	function refresh(): Promise<void> {
		if (closed || stopped) return Promise.resolve();
		if (pending) return pending;
		clearTimeout(retry);
		retry = undefined;
		const current: Promise<void> = snapshot(run).finally(() => {
			if (pending === current) pending = undefined;
		});
		pending = current;
		return current;
	}
	function stop() {
		run++;
		clearTimeout(retry);
		retry = undefined;
		controller?.abort();
		controller = undefined;
		pending = undefined;
		const current = subscription;
		subscription = undefined;
		current?.release();
	}
	const unregister = follow(
		() => {
			// A resume starts under the new generation, with a new snapshot and a new membership.
			stopped = false;
			started = session.generation;
			attempt = 0;
			unauthorizedChecked = false;
			view.status = 'connecting';
			// Initial rows remain visible while this background request obtains the cursor.
			void refresh().catch(() => {});
		},
		() => {
			stopped = true;
			stop();
			view.status = 'paused';
		}
	);
	return {
		refresh,
		close() {
			closed = true;
			unregister();
			stop();
		}
	};
}

/** liveRefresh refreshes page data on patches. Call it during component initialization. */
export function liveRefresh(feed: string, options: LiveRefreshOptions = {}): LiveState {
	let status = $state<LiveStatus>('connecting');
	$effect(() => {
		if (!browser) return;
		return untrack(() =>
			subscribeRefresh(feed, options, (value) => {
				status = value;
			})
		);
	});
	return {
		get status() {
			return status;
		}
	};
}

/** liveList maintains a list from snapshots and patches. Call it during component initialization. */
export function liveList<T extends { id: number | string }>(feed: string, options: LiveListOptions<T> = {}): LiveList<T> {
	const view = $state<LiveListView<T>>({
		rows: options.initial ? [...options.initial] : [],
		status: 'connecting',
		more: false
	});
	let load: (() => Promise<void>) | undefined;
	$effect(() => {
		if (!browser) return;
		return untrack(() => {
			const list = subscribeList(feed, options, view);
			load = list.refresh;
			return () => {
				load = undefined;
				list.close();
			};
		});
	});
	return {
		get rows() {
			return view.rows;
		},
		get status() {
			return view.status;
		},
		get more() {
			return view.more;
		},
		refresh: () => load?.() ?? Promise.resolve()
	};
}
