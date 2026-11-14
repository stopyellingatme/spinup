package live

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

// The problem details of ADR 0033 section 7.5.
const (
	detailDrain        = "The server is shutting down. Try: connect again."
	detailUnauthorized = "A signed-in user is required. Try: sign in, then connect again."
	detailForbidden    = "Your role cannot read this feed. Try: sign in with an account that has a required role."
	detailStart        = "The feed could not start. Try: retry the request later."
	detailCursor       = "The since cursor is not valid. Try: pass the cursor of the snapshot, such as 1731500000000-42, or omit since."
	detailBusy         = "The server is loading other feeds. Try: connect again in a few seconds."
	detailLoad         = "The feed could not load. Try: retry the request later."
)

// The results of the metric live.stream.opened (ADR 0033 section 9).
const (
	resultOK           = "ok"
	resultLimitUser    = "limit_user"
	resultLimitGlobal  = "limit_global"
	resultDrain        = "drain"
	resultBadCursor    = "bad_cursor"
	resultUnauthorized = "unauthorized"
	resultForbidden    = "forbidden"
	resultBusy         = "busy"
	resultError        = "error"
)

// The end reasons of the metric live.stream.duration.
const (
	endAge    = "age"
	endDrain  = "drain"
	endClient = "client"
	endSync   = "sync"
	endWrite  = "write"
	endError  = "error"
)

// The reasons of the sync event (ADR 0033 section 7.7).
const (
	reasonEpoch = "epoch"
	reasonAhead = "ahead"
	reasonGap   = "gap"
	reasonSlow  = "slow"
	reasonReset = "reset"
)

// streamBatch is the most frames that one read takes from a ring.
const streamBatch = 64

// The bye frames are the same for each stream, so the hub encodes them once.
var (
	byeAgeFrame   = byeFrame(endAge)
	byeDrainFrame = byeFrame(endDrain)
)

// Stream returns the handler of the stream route of a feed (ADR 0033 section 7).
// The handler sends server-sent events: ready, then patch events, and at the
// end a sync or a bye event. Pass IsStream to app.WithRequestTimeoutExempt, so
// the request deadline does not end the stream. A nil key panics.
func (h *Hub) Stream(key KeyFunc) http.Handler {
	if key == nil {
		panic("live: Stream requires a KeyFunc.\nTry: pass a function that authorizes the request and returns the key of its feed.")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.serveStream(w, r, key)
	})
}

// serveStream runs the checks of section 7.5 in their order, then the stream.
func (h *Hub) serveStream(w http.ResponseWriter, r *http.Request, keyFunc KeyFunc) {
	if !h.enterStream() {
		h.m.streamOpened("", resultDrain)
		writeProblem(w, r, http.StatusServiceUnavailable, detailDrain, "1")
		return
	}
	defer h.wg.Done()
	key, refused := h.authorize(w, r, keyFunc)
	if refused != "" {
		h.m.streamOpened("", refused)
		return
	}
	feed := key.Feed()
	since, hasSince, ok := streamCursor(r)
	if !ok {
		h.m.streamOpened(feed, resultBadCursor)
		writeProblem(w, r, http.StatusBadRequest, detailCursor, "")
		return
	}
	user, _ := auth.UserFrom(r.Context())
	leave, err := h.admit(user.ID)
	if err != nil {
		if errors.Is(err, errUserLimit) {
			h.m.streamOpened(feed, resultLimitUser)
			writeProblem(w, r, http.StatusTooManyRequests, fmt.Sprintf("This account has %d open live streams. Try: close another tab, then connect again.", h.opts.maxStreamsPerUser), "5")
			return
		}
		h.m.streamOpened(feed, resultLimitGlobal)
		writeProblem(w, r, http.StatusServiceUnavailable, fmt.Sprintf("The server has the most live streams that it allows (%d). Try: connect again in a few seconds.", h.opts.maxStreams), "5")
		return
	}
	defer leave()
	f, err := h.acquire(r.Context(), key)
	if err != nil {
		h.refuseLoad(w, r, feed, err)
		return
	}
	defer h.release(f)
	h.m.streamOpened(feed, resultOK)
	h.m.streamActive(feed, 1)
	defer h.m.streamActive(feed, -1)
	start := time.Now()
	end := h.runStream(w, r, f, since, hasSince)
	h.m.streamEnded(feed, end, time.Since(start))
}

// enterStream counts a stream in wg, unless the hub drains or is closed. The
// check and the count happen under mu, and Drain changes the state under mu
// before it waits, so Drain never misses a stream.
func (h *Hub) enterStream() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == stateDraining || h.state == stateClosed {
		return false
	}
	h.wg.Add(1)
	return true
}

// authorize runs the KeyFunc and validates the key (section 7.5, steps 2 and 3).
// When it refuses the request, it writes the answer and returns the result of
// the metric live.stream.opened. Otherwise the result is empty.
func (h *Hub) authorize(w http.ResponseWriter, r *http.Request, keyFunc KeyFunc) (Key, string) {
	key, err := keyFunc(r)
	switch {
	case err == nil:
	case errors.Is(err, ErrUnauthorized):
		writeProblem(w, r, http.StatusUnauthorized, detailUnauthorized, "")
		return "", resultUnauthorized
	case errors.Is(err, ErrForbidden):
		writeProblem(w, r, http.StatusForbidden, detailForbidden, "")
		return "", resultForbidden
	default:
		h.log.ErrorContext(r.Context(), "live: the key function of the route failed", "path", r.URL.Path, "error", err)
		writeProblem(w, r, http.StatusInternalServerError, detailStart, "")
		return "", resultError
	}
	if err := key.Valid(); err != nil {
		h.log.ErrorContext(r.Context(), "live: the key function of the route returned a key that is not valid", "path", r.URL.Path, "error", err)
		writeProblem(w, r, http.StatusInternalServerError, detailStart, "")
		return "", resultError
	}
	return key, ""
}

// refuseLoad answers a failed acquire (section 7.5, step 6).
func (h *Hub) refuseLoad(w http.ResponseWriter, r *http.Request, feed string, err error) {
	switch {
	case errors.Is(err, errBusy):
		h.m.streamOpened(feed, resultBusy)
		writeProblem(w, r, http.StatusServiceUnavailable, detailBusy, "2")
	case errors.Is(err, errHubClosed):
		h.m.streamOpened(feed, resultDrain)
		writeProblem(w, r, http.StatusServiceUnavailable, detailDrain, "1")
	case r.Context().Err() != nil:
		// The client went away during the load. Nobody reads an answer.
		h.m.streamOpened(feed, resultError)
	default:
		h.m.streamOpened(feed, resultError)
		h.log.ErrorContext(r.Context(), "live: cannot load a feed for a stream", "feed", feed, "error", err)
		writeProblem(w, r, http.StatusServiceUnavailable, detailLoad, "")
	}
}

// streamCursor reads the cursor of the request: the query parameter since,
// or the header Last-Event-ID when since is absent. ok is false for a value
// that is not a cursor.
func streamCursor(r *http.Request) (c Cursor, present, ok bool) {
	raw, present := r.URL.Query()["since"]
	value := ""
	if present {
		value = raw[0]
	} else if value = r.Header.Get("Last-Event-ID"); value != "" {
		present = true
	}
	if !present {
		return Cursor{}, false, true
	}
	c, err := ParseCursor(value)
	return c, true, err == nil
}

// writeProblem writes an application/problem+json answer. retryAfter is the
// value of the Retry-After header, or empty for no header.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, detail, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	httpx.WriteProblem(w, r, httpx.Problem{Status: status, Detail: detail})
}

// open decides the start of a stream (section 7.6) and subscribes it. Both
// happen under the feed lock, so no patch and no reset falls between them.
// A non-empty reason means that the stream sends sync and ends, and s is nil.
// head is the head cursor of the feed at the decision.
func (f *feed) open(since Cursor, hasSince bool) (s *sub, head Cursor, resumed bool, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	head = Cursor{Epoch: f.epoch, Seq: f.seq}
	next := head.Seq + 1
	if hasSince {
		zero := since == Cursor{}
		switch {
		case head == Cursor{}:
			// The feed has no row. Only the cursor 0-0 of an empty snapshot matches it.
			if !zero {
				return nil, head, false, reasonEpoch
			}
		case !zero && since.Epoch != head.Epoch:
			return nil, head, false, reasonEpoch
		case since.Seq > head.Seq:
			return nil, head, false, reasonAhead
		case since.Seq < head.Seq:
			// The ring must hold every patch from since.Seq+1 to the head.
			if f.ring.len() == 0 || f.ring.first() > since.Seq+1 || f.ring.last() != head.Seq {
				return nil, head, false, reasonGap
			}
		}
		next, resumed = since.Seq+1, true
	}
	s = &sub{wake: make(chan struct{}, 1), next: next}
	f.subs[s] = struct{}{}
	// The first read sends the replay, or finds a closed feed.
	wakeSub(s)
	return s, head, resumed, ""
}

// runStream writes the headers, the ready event, and the patches until the
// stream ends. It returns the end reason of the metric live.stream.duration.
func (h *Hub) runStream(w http.ResponseWriter, r *http.Request, f *feed, since Cursor, hasSince bool) string {
	feed := f.key.Feed()
	out := &streamWriter{w: w, rc: http.NewResponseController(w), timeout: h.opts.writeTimeout}
	s, head, resumed, reason := f.open(since, hasSince)
	if s != nil {
		defer f.unsubscribe(s)
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("X-Accel-Buffering", "no")
	header.Del("Content-Length")
	w.WriteHeader(http.StatusOK)
	if err := out.flush(); err != nil {
		if errors.Is(err, http.ErrNotSupported) {
			h.log.ErrorContext(r.Context(), "live: the response writer cannot flush, so the stream cannot start", "feed", feed)
			return endError
		}
		return writeEnd(r)
	}
	if reason != "" {
		h.m.syncSent(feed, reason)
		out.final(syncFrame(head, reason))
		return endSync
	}
	if err := out.send(readyFrame(head, resumed)); err != nil {
		return writeEnd(r)
	}
	frames := make([][]byte, streamBatch)
	heartbeat := time.NewTicker(h.opts.heartbeat)
	defer heartbeat.Stop()
	age := time.NewTimer(streamAge(h.opts.maxStreamAge))
	defer age.Stop()
	done := r.Context().Done()
	for {
		select {
		case <-s.wake:
			n, state := f.read(s, frames)
			switch state {
			case readGap:
				return h.endSync(out, f, reasonSlow)
			case readReset:
				return h.endSync(out, f, reasonReset)
			case readClosed:
				out.final(byeDrainFrame)
				return endDrain
			}
			if n == 0 {
				continue
			}
			err := out.batch(frames[:n])
			// Drop the references, so a frame that leaves the ring can be freed.
			clear(frames[:n])
			if err != nil {
				return writeEnd(r)
			}
			h.m.patchDelivered(feed, int64(n))
			heartbeat.Reset(h.opts.heartbeat)
			if n == len(frames) {
				// The ring may hold more. The next turn of the loop reads it, so
				// the drain, the age, and the client stay responsive in a burst.
				wakeSub(s)
			}
		case <-heartbeat.C:
			if err := out.send(pingFrame); err != nil {
				return writeEnd(r)
			}
		case <-age.C:
			out.final(byeAgeFrame)
			return endAge
		case <-h.drainCh:
			out.final(byeDrainFrame)
			return endDrain
		case <-done:
			return endClient
		}
	}
}

// endSync sends sync with the head of the feed and the reason.
func (h *Hub) endSync(out *streamWriter, f *feed, reason string) string {
	h.m.syncSent(f.key.Feed(), reason)
	out.final(syncFrame(f.head(), reason))
	return endSync
}

// writeEnd returns the end reason of a failed write: client when the client
// went away, and write otherwise.
func writeEnd(r *http.Request) string {
	if r.Context().Err() != nil {
		return endClient
	}
	return endWrite
}

// streamAge returns a random age between 80 and 100 percent of d, so the
// reconnects of many clients spread out (section 7.9).
func streamAge(d time.Duration) time.Duration {
	return d - d/5 + rand.N(d/5+1) //nolint:gosec // jitter needs no secure random source
}

// streamWriter writes frames with a write deadline for each batch (section 7.8).
// The deadline covers one write and its flush. After a successful flush the
// writer clears it: with HTTP/2 an armed deadline resets an idle stream even
// when no write waits, so a deadline must not stay armed between writes.
type streamWriter struct {
	w          http.ResponseWriter
	rc         *http.ResponseController
	timeout    time.Duration
	noDeadline bool
}

// deadline sets the write deadline to now plus the write timeout. A writer
// with no deadline support writes with no deadline.
func (o *streamWriter) deadline() error {
	if o.noDeadline {
		return nil
	}
	err := o.rc.SetWriteDeadline(time.Now().Add(o.timeout))
	if errors.Is(err, http.ErrNotSupported) {
		o.noDeadline = true
		return nil
	}
	return err
}

// flushed flushes the written bytes, then clears the write deadline.
func (o *streamWriter) flushed() error {
	if err := o.rc.Flush(); err != nil {
		return err
	}
	if o.noDeadline {
		return nil
	}
	return o.rc.SetWriteDeadline(time.Time{})
}

func (o *streamWriter) flush() error {
	if err := o.deadline(); err != nil {
		return err
	}
	return o.flushed()
}

// send writes one frame and flushes it.
func (o *streamWriter) send(frame []byte) error {
	if err := o.deadline(); err != nil {
		return err
	}
	if _, err := o.w.Write(frame); err != nil {
		return err
	}
	return o.flushed()
}

// batch writes the frames and flushes them once.
func (o *streamWriter) batch(frames [][]byte) error {
	if err := o.deadline(); err != nil {
		return err
	}
	for _, frame := range frames {
		if _, err := o.w.Write(frame); err != nil {
			return err
		}
	}
	return o.flushed()
}

// final writes the last frame of a stream. The stream ends after it, so an
// error changes nothing.
func (o *streamWriter) final(frame []byte) { _ = o.send(frame) }
