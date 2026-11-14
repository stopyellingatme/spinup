// Managed by spinup: `spinup upgrade` keeps this file current.
import { browser } from '$app/environment';
import { invalidate, invalidateAll } from '$app/navigation';
import { untrack } from 'svelte';
import { LiveClient, backoff } from './live.core';
import type { Cursor, LiveStatus, Patch } from './live.core';

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

type Subscriber = {
	status: (status: LiveStatus) => void;
	ready: (cursor: Cursor, resumed: boolean) => void;
	patch: (patch: Patch<unknown>) => void;
	sync: () => Promise<void>;
};
type Feed = {
	client: LiveClient<unknown>;
	cursor: Cursor | null;
	status: LiveStatus;
	users: number;
	subscribers: Set<Subscriber>;
	syncing: boolean;
	restart: () => void;
	dispose: () => void;
};
const feeds = new Map<string, Feed>();

function createFeed(name: string, cursor: Cursor | null): Feed {
	let hiddenTimer: ReturnType<typeof setTimeout> | undefined;
	let hiddenPaused = false;
	let offline = !navigator.onLine;
	let disposed = false;
	let unauthorizedInvalidated = false;
	const blocked = () => hiddenPaused || offline;
	const entry: Feed = {
		cursor, status: 'connecting', users: 0, subscribers: new Set(), syncing: false,
		client: new LiveClient<unknown>({
			feed: name,
			getCursor: () => entry.cursor,
			onStatus(status) {
				entry.status = status;
				for (const subscriber of entry.subscribers) subscriber.status(status);
			},
			onReady(cursor, resumed) {
				if (!resumed || !entry.cursor) entry.cursor = cursor;
				for (const subscriber of entry.subscribers) subscriber.ready(cursor, resumed);
			},
			onPatch(patch) {
				entry.cursor = { epoch: patch.epoch, seq: patch.seq };
				for (const subscriber of entry.subscribers) subscriber.patch(patch);
			},
			onSync(reason) {
				if (reason === 'unauthorized') {
					if (!unauthorizedInvalidated) {
						unauthorizedInvalidated = true;
						void invalidateAll().catch(() => {});
					}
					return;
				}
				entry.cursor = null;
				entry.syncing = true;
				void Promise.all([...entry.subscribers].map((subscriber) => subscriber.sync()))
					.then(() => {
						entry.syncing = false;
						if (!disposed) entry.restart();
					}, () => { entry.syncing = false; });
			}
		}),
		restart() {
			if (disposed || entry.syncing || entry.status === 'unauthorized') return;
			entry.client.stop();
			// A snapshot can complete while the tab is offline or hidden.
			if (blocked()) {
				entry.status = 'paused';
				for (const subscriber of entry.subscribers) subscriber.status('paused');
				return;
			}
			entry.client.start();
		},
		dispose() {
			disposed = true;
			entry.client.stop();
			clearTimeout(hiddenTimer);
			document.removeEventListener('visibilitychange', visibility);
			window.removeEventListener('offline', goOffline);
			window.removeEventListener('online', goOnline);
		}
	};
	function resume() {
		if (blocked() || disposed || entry.syncing || entry.status === 'unauthorized') return;
		entry.client.resume();
		// restart may have deferred the first request while offline.
		if (entry.status === 'paused') entry.restart();
	}
	function visibility() {
		clearTimeout(hiddenTimer);
		hiddenTimer = undefined;
		if (document.visibilityState === 'hidden') {
			hiddenTimer = setTimeout(() => {
				hiddenTimer = undefined;
				if (document.visibilityState === 'hidden') {
					hiddenPaused = true;
					entry.client.pause();
				}
			}, 60000);
		} else {
			hiddenPaused = false;
			resume();
		}
	}
	function goOffline() {
		offline = true;
		entry.client.pause();
	}
	function goOnline() {
		offline = false;
		resume();
	}
	document.addEventListener('visibilitychange', visibility);
	window.addEventListener('offline', goOffline);
	window.addEventListener('online', goOnline);
	visibility();
	return entry;
}

function join(feed: string, subscriber: Subscriber, cursor: Cursor | null) {
	let entry = feeds.get(feed);
	const fresh = !entry;
	if (!entry) {
		entry = createFeed(feed, cursor);
		feeds.set(feed, entry);
	}
	entry.users++;
	entry.subscribers.add(subscriber);
	subscriber.status(entry.status);
	if (cursor) entry.cursor = cursor;
	// A new snapshot can predate an event received by an existing stream. Replay from it.
	if (fresh || cursor) entry.restart();
	const joined = entry;
	return {
		snapshot(cursor: Cursor) {
			joined.cursor = cursor;
			joined.restart();
		},
		release() {
			if (!joined.subscribers.delete(subscriber)) return;
			if (--joined.users === 0) {
				joined.dispose();
				feeds.delete(feed);
			}
		}
	};
}

/** liveRefresh refreshes page data on patches. Call it during component initialization. */
export function liveRefresh(feed: string, options: LiveRefreshOptions = {}): LiveState {
	let status = $state<LiveStatus>('connecting');
	$effect(() => {
		if (!browser) return;
		return untrack(() => {
			let active = true;
			let trailing: ReturnType<typeof setTimeout> | undefined;
			let maximum: ReturnType<typeof setTimeout> | undefined;
			function clear() {
				clearTimeout(trailing);
				clearTimeout(maximum);
				trailing = maximum = undefined;
			}
			async function refresh() {
				clear();
				if (!active) return;
				if (typeof options.invalidate === 'function') await options.invalidate();
				else if (typeof options.invalidate === 'string') await invalidate(options.invalidate);
				else await invalidateAll();
			}
			function schedule() {
				clearTimeout(trailing);
				const run = () => { void refresh().catch(() => {}); };
				trailing = setTimeout(run, options.debounceMs ?? 200);
				maximum ??= setTimeout(run, options.maxWaitMs ?? 2000);
			}
			const subscription = join(feed, {
				status: (value) => {
					status = value;
					if (value === 'unauthorized') clear();
				},
				ready: (_cursor, resumed) => { if (!resumed) schedule(); },
				patch: schedule,
				sync: refresh
			}, null);
			return () => {
				active = false;
				clear();
				subscription.release();
			};
		});
	});
	return { get status() { return status; } };
}

/** liveList maintains a list from snapshots and patches. Call it during component initialization. */
export function liveList<T extends { id: number | string }>(feed: string, options: LiveListOptions<T> = {}): LiveList<T> {
	let rows = $state<T[]>(options.initial ? [...options.initial] : []);
	let status = $state<LiveStatus>('connecting');
	let more = $state(false);
	let load: (() => Promise<void>) | undefined;
	$effect(() => {
		if (!browser) return;
		return untrack(() => {
			let active = true;
			let attempt = 0;
			let retry: ReturnType<typeof setTimeout> | undefined;
			let controller: AbortController | undefined;
			let pending: Promise<void> | undefined;
			let subscription: ReturnType<typeof join> | undefined;
			let unauthorizedInvalidated = false;
			function patch(patch: Patch<unknown>) {
				for (const change of patch.changes) {
					if (change.op === 'touch') {
						void refresh().catch(() => {});
						continue;
					}
					if (change.op === 'delete') rows = rows.filter((row) => String(row.id) !== change.id);
					else {
						const row = change.row as T;
						const index = rows.findIndex((value) => String(value.id) === String(row.id));
						if (index < 0) rows.push(row);
						else rows[index] = row;
						if (options.sort) rows.sort(options.sort);
						rows = rows.slice(0, options.limit ?? 500);
					}
				}
			}
			async function snapshot() {
				controller = new AbortController();
				try {
					const response = await fetch(`/api/live/${encodeURIComponent(feed)}`, {
						credentials: 'same-origin', signal: controller.signal
					});
					if (!active) return;
					if (response.status === 401 || response.status === 403) {
						status = 'unauthorized';
						subscription?.release();
						subscription = undefined;
						if (response.status === 401 && !unauthorizedInvalidated) {
							unauthorizedInvalidated = true;
							void invalidateAll().catch(() => {});
						}
						return;
					}
					if (!response.ok) throw new Error('The live snapshot failed. Try: refresh the list.');
					const data: { epoch: number; seq: number; rows: T[]; more?: boolean } = await response.json();
					if (!active) return;
					rows = data.rows;
					more = data.more === true;
					attempt = 0;
					const cursor = { epoch: data.epoch, seq: data.seq };
					if (subscription) subscription.snapshot(cursor);
					else subscription = join(feed, {
						status: (value) => {
							status = value;
							if (value === 'unauthorized') {
								clearTimeout(retry);
								retry = undefined;
								controller?.abort();
							}
						},
						ready: () => {}, patch, sync: refresh
					}, cursor);
				} catch (error) {
					if (!active || status === 'unauthorized') return;
					status = 'reconnecting';
					retry = setTimeout(() => {
						retry = undefined;
						void refresh().catch(() => {});
					}, backoff(attempt++, Math.random));
					throw error;
				}
			}
			function refresh(): Promise<void> {
				if (!active) return Promise.resolve();
				if (pending) return pending;
				clearTimeout(retry);
				retry = undefined;
				pending = snapshot().finally(() => { pending = undefined; });
				return pending;
			}
			load = refresh;
			// Initial rows remain visible while this background request obtains the cursor.
			void refresh().catch(() => {});
			return () => {
				active = false;
				load = undefined;
				clearTimeout(retry);
				controller?.abort();
				subscription?.release();
			};
		});
	});
	return {
		get rows() { return rows; },
		get status() { return status; },
		get more() { return more; },
		refresh: () => load?.() ?? Promise.resolve()
	};
}
