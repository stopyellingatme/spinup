package live

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// fetchMark is the state of a feed in the dirty set.
type fetchMark uint8

const (
	markQueued  fetchMark = iota + 1 // in pending, no fetch runs
	markRunning                      // a fetch runs
	markRerun                        // a fetch runs, and a mark came during it
)

const (
	// fetchBatch is the most rows of one fetch statement (section 6.6).
	fetchBatch = 256
	// pollChunk is the most keys of one poll statement (section 6.8).
	pollChunk = 500
	// queryTimeout limits one fetch, load, or poll.
	queryTimeout = 10 * time.Second
	// pruneTimeout limits the Prune call of the janitor.
	pruneTimeout = time.Minute
	// minRingPatches is the number of patches that the byte limit keeps in a ring.
	minRingPatches = 32
)

const (
	feedSQL  = `SELECT epoch, seq FROM live_feeds WHERE key = $1`
	loadSQL  = `SELECT seq, changes::text FROM live_outbox WHERE feed = $1 ORDER BY seq DESC LIMIT $2`
	fetchSQL = `SELECT seq, changes::text FROM live_outbox WHERE feed = $1 AND seq > $2 ORDER BY seq LIMIT 256`
	pollSQL  = `SELECT key, epoch, seq FROM live_feeds WHERE key = ANY($1)`
)

var readOnlyTx = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

// mark puts the feed in the dirty set. One feed has at most one fetch at a
// time. A mark during a fetch runs the feed again after the fetch.
func (h *Hub) mark(key Key) {
	h.dmu.Lock()
	switch h.fetchState[key] {
	case 0:
		h.fetchState[key] = markQueued
		h.pending = append(h.pending, key)
	case markRunning:
		h.fetchState[key] = markRerun
	}
	h.dmu.Unlock()
	select {
	case h.work <- struct{}{}:
	default:
	}
}

// next takes the next feed of the dirty set.
func (h *Hub) next() (Key, bool) {
	h.dmu.Lock()
	defer h.dmu.Unlock()
	if len(h.pending) == 0 {
		return "", false
	}
	key := h.pending[0]
	h.pending[0] = ""
	h.pending = h.pending[1:]
	h.fetchState[key] = markRunning
	return key, true
}

// done ends the fetch of key and queues it again when a mark came during it.
func (h *Hub) done(key Key) {
	h.dmu.Lock()
	if h.fetchState[key] == markRerun {
		h.fetchState[key] = markQueued
		h.pending = append(h.pending, key)
		h.dmu.Unlock()
		select {
		case h.work <- struct{}{}:
		default:
		}
		return
	}
	delete(h.fetchState, key)
	h.dmu.Unlock()
}

// fetchWorker runs fetches until ctx ends. A worker empties the dirty set
// before it waits, so no mark is lost.
func (h *Hub) fetchWorker(ctx context.Context) {
	defer h.loops.Done()
	for {
		for {
			key, ok := h.next()
			if !ok {
				break
			}
			if f := h.tracked(key); f != nil {
				h.fetch(ctx, f)
			}
			h.done(key)
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-h.work:
		}
	}
}

// fetch reads the new patches of f and appends them to its ring (section
// 6.6). An epoch change, a seq below the head, a missing row, or an append gap
// resets the feed.
func (h *Hub) fetch(ctx context.Context, f *feed) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	ctx, span := h.m.start(ctx, "live.load", feedAttribute(string(f.key)))
	defer span.End()
	if err := h.fetchTx(ctx, f); err != nil {
		failSpan(span, err)
		if ctx.Err() == nil {
			h.log.WarnContext(ctx, "live: cannot read the new patches of a feed", "feed", f.key.Feed(), "error", err)
		}
	}
}

func (h *Hub) fetchTx(ctx context.Context, f *feed) error {
	tx, err := h.pool.BeginTx(ctx, readOnlyTx)
	if err != nil {
		return dbError("fetch patches", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var epoch, seq int64
	if err := tx.QueryRow(ctx, feedSQL, string(f.key)).Scan(&epoch, &seq); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The feed has no row: no patch to read. A later publish makes a new epoch.
			return nil
		}
		return dbError("fetch patches", err)
	}
	head := f.head()
	empty := head.Epoch == 0 && head.Seq == 0
	switch {
	case !empty && epoch != head.Epoch, seq < head.Seq:
		return h.resetFeed(ctx, tx, f, epoch, seq)
	case seq == head.Seq:
		return nil
	}
	after := head.Seq
	var frames []pendingFrame
	for {
		frames = frames[:0]
		rows, err := tx.Query(ctx, fetchSQL, string(f.key), after)
		if err != nil {
			return dbError("fetch patches", err)
		}
		for rows.Next() {
			var n int64
			var changes []byte
			if err := rows.Scan(&n, &changes); err != nil {
				rows.Close()
				return dbError("fetch patches", err)
			}
			frames = append(frames, pendingFrame{seq: n, frame: patchFrame(epoch, n, changes)})
		}
		if err := rows.Err(); err != nil {
			return dbError("fetch patches", err)
		}
		if len(frames) == 0 {
			break
		}
		appended, result := h.appendFrames(f, epoch, frames)
		if appended > 0 {
			h.m.patchReceived(string(f.key), int64(appended))
		}
		if result == appendGap {
			return h.resetFeed(ctx, tx, f, epoch, seq)
		}
		after = frames[len(frames)-1].seq
		if len(frames) < fetchBatch {
			break
		}
	}
	if after < seq {
		// The snapshot of the transaction has the feed at seq, but a row is missing.
		return h.resetFeed(ctx, tx, f, epoch, seq)
	}
	return nil
}

// pendingFrame is one encoded patch before it goes into a ring.
type pendingFrame struct {
	seq   int64
	frame []byte
}

// appendFrames appends the frames to f under the hub lock, so the ring byte
// sum stays exact. It stops at the first result that is not appendOK.
// A pressure trim can end idle feeds: their notification marks go after the
// hub lock is released.
func (h *Hub) appendFrames(f *feed, epoch int64, frames []pendingFrame) (int, appendResult) {
	h.mu.Lock()
	if h.feeds[f.key] != f {
		// The janitor ended the feed during the fetch.
		h.mu.Unlock()
		return 0, appendStale
	}
	before := f.ring.bytes()
	appended := 0
	result := appendOK
	for _, p := range frames {
		result = f.append(epoch, p.seq, p.frame)
		if result != appendOK {
			break
		}
		appended++
	}
	h.ringBytes += f.ring.bytes() - before
	var ended []Key
	if h.ringBytes > h.opts.ringBytes {
		ended = h.trimRingsLocked()
	}
	h.mu.Unlock()
	h.forget(ended)
	return appended, result
}

// resetFeed resets f to the epoch: the ring empties, and each stream reads
// readReset. Then the ring loads again from the outbox in the same snapshot,
// so a client that loads the snapshot again can resume.
func (h *Hub) resetFeed(ctx context.Context, tx pgx.Tx, f *feed, epoch, seq int64) error {
	frames, err := h.readRing(ctx, tx, f.key, epoch, seq)
	if err != nil {
		return err
	}
	if h.applyReset(f, epoch, seq, frames) {
		h.log.InfoContext(ctx, "live: a feed was reset", "feed", f.key.Feed(), "epoch", epoch, "seq", seq)
	}
	return nil
}

// applyReset resets f to the epoch and fills its ring with the frames (in
// descending order). The clear, the fill, the reset mark of each sub, and the
// wake happen under one hold of f.mu. A stream that opens during the reset
// thus sees the old feed (and reads readReset) or the full new feed, never an
// empty ring with the head 0. It returns false when the hub no longer tracks f.
func (h *Hub) applyReset(f *feed, epoch, seq int64, frames []pendingFrame) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.feeds[f.key] != f {
		return false
	}
	before := f.ring.bytes()
	f.mu.Lock()
	f.ring.clear()
	if h.opts.duringReset != nil {
		h.opts.duringReset(f)
	}
	fill(f, epoch, seq, frames)
	for s := range f.subs {
		s.reset = true
	}
	f.wakeAll()
	f.mu.Unlock()
	h.ringBytes += f.ring.bytes() - before
	return true
}

// readRing reads the newest patches of the feed, up to the ring size, in
// descending order. It keeps only the rows that follow on from seq with no
// gap, so the result is the top of the feed.
func (h *Hub) readRing(ctx context.Context, tx pgx.Tx, key Key, epoch, seq int64) ([]pendingFrame, error) {
	if seq == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, loadSQL, string(key), min(int64(h.opts.ringPatches), seq))
	if err != nil {
		return nil, dbError("load feed", err)
	}
	defer rows.Close()
	frames := make([]pendingFrame, 0, min(int64(h.opts.ringPatches), seq))
	want := seq
	for rows.Next() {
		var n int64
		var changes []byte
		if err := rows.Scan(&n, &changes); err != nil {
			return nil, dbError("load feed", err)
		}
		if n != want {
			break
		}
		frames = append(frames, pendingFrame{seq: n, frame: patchFrame(epoch, n, changes)})
		want--
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("load feed", err)
	}
	return frames, nil
}

// fill sets the head of f and puts the frames (in descending order) into its
// ring in ascending order. It requires f.mu and an empty ring.
func fill(f *feed, epoch, seq int64, frames []pendingFrame) {
	f.epoch, f.seq = epoch, seq
	for i := len(frames) - 1; i >= 0; i-- {
		f.ring.push(frames[i].seq, frames[i].frame)
	}
}

// load reads the feed under a bootstrap slot (section 6.5), in a read-only
// transaction with the isolation REPEATABLE READ. A feed with no row has the
// epoch 0 and the head 0.
func (h *Hub) load(ctx context.Context, f *feed) error {
	release, err := h.slot(ctx)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	ctx, span := h.m.start(ctx, "live.load", feedAttribute(string(f.key)))
	defer span.End()
	frames, epoch, seq, err := h.loadTx(ctx, f.key)
	if err != nil {
		failSpan(span, err)
		return err
	}
	h.mu.Lock()
	f.mu.Lock()
	fill(f, epoch, seq, frames)
	f.mu.Unlock()
	h.ringBytes += f.ring.bytes()
	var ended []Key
	if h.ringBytes > h.opts.ringBytes {
		ended = h.trimRingsLocked()
	}
	h.mu.Unlock()
	h.forget(ended)
	return nil
}

func (h *Hub) loadTx(ctx context.Context, key Key) (frames []pendingFrame, epoch, seq int64, err error) {
	tx, err := h.pool.BeginTx(ctx, readOnlyTx)
	if err != nil {
		return nil, 0, 0, dbError("load feed", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	err = tx.QueryRow(ctx, feedSQL, string(key)).Scan(&epoch, &seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, 0, nil
	}
	if err != nil {
		return nil, 0, 0, dbError("load feed", err)
	}
	frames, err = h.readRing(ctx, tx, key, epoch, seq)
	return frames, epoch, seq, err
}

// poll reads the position of every tracked feed (section 6.8) and marks a
// feed dirty when its epoch differs from the ring or its seq is above the
// head. A patch that no notification announced counts in live.poll.recovered.
func (h *Hub) poll(ctx context.Context) {
	h.mu.Lock()
	keys := make([]string, 0, len(h.feeds))
	for k, f := range h.feeds {
		if isReady(f) {
			keys = append(keys, string(k))
		}
	}
	h.mu.Unlock()
	slices.Sort(keys)
	for chunk := range slices.Chunk(keys, pollChunk) {
		if err := h.pollChunk(ctx, chunk); err != nil {
			if ctx.Err() == nil {
				h.log.WarnContext(ctx, "live: the poll failed", "error", err)
			}
			return
		}
	}
}

func (h *Hub) pollChunk(ctx context.Context, keys []string) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := h.pool.Query(ctx, pollSQL, keys)
	if err != nil {
		return dbError("poll feeds", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var epoch, seq int64
		if err := rows.Scan(&key, &epoch, &seq); err != nil {
			return dbError("poll feeds", err)
		}
		f := h.tracked(Key(key))
		if f == nil {
			continue
		}
		head := f.head()
		empty := head.Epoch == 0 && head.Seq == 0
		if epoch == head.Epoch && seq <= head.Seq {
			continue
		}
		if epoch == head.Epoch || empty {
			h.dmu.Lock()
			known := max(head.Seq, h.notified[Key(key)])
			h.notified[Key(key)] = max(h.notified[Key(key)], seq)
			h.dmu.Unlock()
			if seq > known {
				h.m.pollRecovered(seq - known)
			}
		}
		h.mark(Key(key))
	}
	if err := rows.Err(); err != nil {
		return dbError("poll feeds", err)
	}
	return nil
}

// janitor ends idle feeds after IdleTTL, keeps the ring bytes under the limit,
// and calls Prune every 5 minutes, until ctx ends.
func (h *Hub) janitor(ctx context.Context) {
	defer h.loops.Done()
	idle := time.NewTicker(min(max(h.opts.idleTTL/2, 10*time.Millisecond), 15*time.Second))
	defer idle.Stop()
	rings := time.NewTicker(h.opts.ringCheck)
	defer rings.Stop()
	prune := time.NewTicker(h.opts.pruneEvery)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
			h.sweepIdle(time.Now())
		case <-rings.C:
			h.trimRings()
		case <-prune.C:
			h.prune(ctx)
		}
	}
}

// sweepIdle ends the feeds that have had no stream for IdleTTL.
func (h *Hub) sweepIdle(now time.Time) {
	var ended []Key
	h.mu.Lock()
	for k, f := range h.feeds {
		if f.refs == 0 && isReady(f) && !f.idleSince.IsZero() && now.Sub(f.idleSince) >= h.opts.idleTTL {
			h.dropFeedLocked(k, f)
			ended = append(ended, k)
		}
	}
	h.mu.Unlock()
	h.forget(ended)
}

// dropFeedLocked removes a feed from the hub. It requires h.mu.
func (h *Hub) dropFeedLocked(k Key, f *feed) {
	delete(h.feeds, k)
	h.ringBytes -= f.ring.bytes()
	f.close()
}

// forget removes the notification marks of ended feeds.
func (h *Hub) forget(keys []Key) {
	if len(keys) == 0 {
		return
	}
	h.dmu.Lock()
	for _, k := range keys {
		delete(h.notified, k)
	}
	h.dmu.Unlock()
}

// trimRings keeps the sum of the ring bytes under the limit (section 6.2).
func (h *Hub) trimRings() {
	h.mu.Lock()
	ended := h.trimRingsLocked()
	h.mu.Unlock()
	h.forget(ended)
}

// trimRingsLocked first ends the idle feeds, the one that has been idle the
// longest first. Then it drops the oldest patches of the largest ring, but it
// keeps at least 32 patches in each ring. It requires h.mu, and it returns the
// keys of the feeds that it ended.
func (h *Hub) trimRingsLocked() []Key {
	var total int64
	for _, f := range h.feeds {
		total += f.ring.bytes()
	}
	h.ringBytes = total
	if total <= h.opts.ringBytes {
		return nil
	}
	var idle []*feed
	for _, f := range h.feeds {
		if f.refs == 0 && isReady(f) {
			idle = append(idle, f)
		}
	}
	slices.SortFunc(idle, func(a, b *feed) int {
		return cmp.Or(a.idleSince.Compare(b.idleSince), cmp.Compare(a.key, b.key))
	})
	var ended []Key
	for _, f := range idle {
		if h.ringBytes <= h.opts.ringBytes {
			return ended
		}
		h.dropFeedLocked(f.key, f)
		ended = append(ended, f.key)
	}
	for h.ringBytes > h.opts.ringBytes {
		var largest *feed
		for _, f := range h.feeds {
			if f.ring.len() > minRingPatches && (largest == nil || f.ring.bytes() > largest.ring.bytes()) {
				largest = f
			}
		}
		if largest == nil {
			return ended
		}
		largest.mu.Lock()
		for h.ringBytes > h.opts.ringBytes && largest.ring.len() > minRingPatches {
			h.ringBytes -= largest.ring.dropOldest()
		}
		largest.mu.Unlock()
	}
	return ended
}

// prune runs Prune with the retention of the hub. Every instance prunes.
func (h *Hub) prune(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, pruneTimeout)
	defer cancel()
	n, err := Prune(ctx, h.pool, PruneOptions{Patches: h.opts.retention, Feeds: pruneFeedAge})
	if err != nil {
		if ctx.Err() == nil {
			h.log.WarnContext(ctx, "live: cannot prune the outbox", "error", err)
		}
		return
	}
	if n.Patches > 0 || n.Feeds > 0 {
		h.log.DebugContext(ctx, "live: pruned the outbox", "patches", n.Patches, "feeds", n.Feeds)
	}
}
