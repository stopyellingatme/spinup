package live

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"time"
)

// detailSnapshot is the problem detail of a failed snapshot read.
const detailSnapshot = "The snapshot could not load. Try: retry the request later."

// snapshotBody is the JSON answer of a snapshot (ADR 0033 section 7.3).
type snapshotBody[T any] struct {
	Epoch int64 `json:"epoch"`
	Seq   int64 `json:"seq"`
	Rows  []T   `json:"rows"`
	More  bool  `json:"more,omitempty"`
}

// moreKey is the context key of the more mark of a snapshot load.
type moreKey struct{}

// MarkMore marks the snapshot that ctx loads as cut: the answer has
// "more":true. A load function calls it when it returns fewer rows than the
// source holds, such as the first page of a longer list. Outside the load
// function of Snapshot it does nothing.
func MarkMore(ctx context.Context) {
	if more, ok := ctx.Value(moreKey{}).(*atomic.Bool); ok {
		more.Store(true)
	}
}

// Snapshot returns the handler of the snapshot route of a feed (ADR 0033
// section 7.3). It reads the cursor of the feed first and then calls load, so
// the rows are at least as new as the cursor. The answer is
// {"epoch":E,"seq":N,"rows":[...]}, with "more":true when load returned more
// than WithSnapshotLimit rows or called MarkMore. load gets the limit plus
// one. The stream route
// of the feed must use the same key. A nil hub, key, or load panics.
func Snapshot[T any](h *Hub, key KeyFunc, load func(ctx context.Context, key Key, limit int) ([]T, error)) http.Handler {
	if h == nil || key == nil || load == nil {
		panic("live: Snapshot requires a hub, a KeyFunc, and a load function.\nTry: pass the hub of the app, the KeyFunc of the stream route, and a function that reads the rows.")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k, refused := h.authorize(w, r, key)
		if refused != "" {
			return
		}
		start := time.Now()
		ctx, span := h.m.start(r.Context(), "live.snapshot", feedAttribute(string(k)))
		defer span.End()
		body, err := readSnapshot(ctx, h, k, load)
		if err != nil {
			failSpan(span, err)
			h.m.snapshotDone(string(k), time.Since(start), err)
			switch {
			case errors.Is(err, errBusy):
				writeProblem(w, r, http.StatusServiceUnavailable, detailBusy, "2")
			case r.Context().Err() != nil:
				// The client went away. Nobody reads an answer.
			default:
				h.log.ErrorContext(ctx, "live: cannot read a snapshot", "feed", k.Feed(), "error", err)
				writeProblem(w, r, http.StatusServiceUnavailable, detailSnapshot, "")
			}
			return
		}
		data, err := json.Marshal(body)
		h.m.snapshotDone(string(k), time.Since(start), err)
		if err != nil {
			failSpan(span, err)
			h.log.ErrorContext(ctx, "live: cannot encode a snapshot", "feed", k.Feed(), "error", err)
			writeProblem(w, r, http.StatusInternalServerError, detailSnapshot, "")
			return
		}
		header := w.Header()
		header.Set("Content-Type", "application/json")
		header.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append(data, '\n'))
	})
}

// readSnapshot takes a bootstrap slot, reads the cursor, and then the rows.
// The slot is free again before the answer is written to the client.
func readSnapshot[T any](ctx context.Context, h *Hub, k Key, load func(ctx context.Context, key Key, limit int) ([]T, error)) (snapshotBody[T], error) {
	var body snapshotBody[T]
	release, err := h.slot(ctx)
	if err != nil {
		return body, err
	}
	defer release()
	cursor, err := h.readCursor(ctx, k)
	if err != nil {
		return body, err
	}
	limit := h.opts.snapshotLimit
	var more atomic.Bool
	rows, err := load(context.WithValue(ctx, moreKey{}, &more), k, limit+1)
	if err != nil {
		return body, &Error{Op: "load snapshot rows", Err: err, Try: "check the load function of the snapshot route"}
	}
	body.More = more.Load()
	if len(rows) > limit {
		rows, body.More = rows[:limit], true
	}
	if rows == nil {
		rows = []T{}
	}
	body.Epoch, body.Seq, body.Rows = cursor.Epoch, cursor.Seq, rows
	return body, nil
}
