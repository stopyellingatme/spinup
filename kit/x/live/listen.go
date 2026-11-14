package live

import (
	"context"
	"errors"
	mathrand "math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// notifyChannel is the channel of the trigger live_outbox_notify.
const notifyChannel = "spinup_live"

// connTimeout limits a ping and the close of the LISTEN connection.
const connTimeout = 5 * time.Second

// connectListener opens the dedicated LISTEN connection (ADR 0033 section
// 6.7). The connection is not part of the pool, and it has no tracer.
func (h *Hub) connectListener(ctx context.Context) (*pgx.Conn, error) {
	cfg := h.pool.Config().ConnConfig.Copy()
	cfg.Tracer = nil
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		closeConn(conn)
		return nil, err
	}
	h.listenPID.Store(conn.PgConn().PID())
	h.listenerUp.Store(true)
	return conn, nil
}

// closeConn closes a connection with a short limit.
func closeConn(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), connTimeout)
	defer cancel()
	_ = conn.Close(ctx)
}

// listenLoop runs the loop of section 6.7 until ctx ends. conn is the
// connection of the first attempt, or nil. Without a connection the loop
// polls every PollInterval and opens the connection again with a backoff.
func (h *Hub) listenLoop(ctx context.Context, conn *pgx.Conn) {
	defer h.loops.Done()
	defer func() {
		if conn != nil {
			closeConn(conn)
		}
		h.listenerUp.Store(false)
	}()
	if h.opts.noListen {
		h.pollLoop(ctx)
		return
	}
	attempt := 0
	lastPoll := time.Now()
	for ctx.Err() == nil {
		if conn == nil {
			if !h.reconnectWait(ctx, attempt, &lastPoll) {
				return
			}
			attempt++
			if h.opts.beforeDial != nil {
				h.opts.beforeDial()
			}
			connectCtx, cancel := context.WithTimeout(ctx, startListenTimeout)
			c, err := h.connectListener(connectCtx)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					h.log.WarnContext(ctx, "live: cannot open the LISTEN connection; the hub polls and tries again", "error", err, "attempt", attempt)
				}
				continue
			}
			conn, attempt = c, 0
			h.log.InfoContext(ctx, "live: the LISTEN connection is open")
			// Notifications that came while the connection was down are lost.
			h.poll(ctx)
			lastPoll = time.Now()
			continue
		}
		waitCtx, cancel := context.WithTimeout(ctx, h.opts.pollInterval)
		n, err := conn.WaitForNotification(waitCtx)
		cancel()
		switch {
		case err == nil:
			h.notify(n.Payload)
			// A steady flow of notifications must not stop the poll.
			if time.Since(lastPoll) >= h.opts.pollInterval {
				h.poll(ctx)
				lastPoll = time.Now()
			}
		case ctx.Err() != nil:
			return
		case errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err):
			// The connection stays in use after a timeout (claim a). A dead
			// connection fails the ping.
			pingCtx, cancel := context.WithTimeout(ctx, connTimeout)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				h.dropListener(ctx, conn, err)
				conn = nil
			}
			h.poll(ctx)
			lastPoll = time.Now()
		default:
			h.dropListener(ctx, conn, err)
			conn = nil
		}
	}
}

// dropListener closes a failed LISTEN connection.
func (h *Hub) dropListener(ctx context.Context, conn *pgx.Conn, err error) {
	h.listenerUp.Store(false)
	h.log.WarnContext(ctx, "live: the LISTEN connection failed; the hub polls and connects again", "error", err)
	closeConn(conn)
}

// reconnectWait waits the backoff of the attempt. It polls every PollInterval
// while it waits. It returns false when ctx ends.
func (h *Hub) reconnectWait(ctx context.Context, attempt int, lastPoll *time.Time) bool {
	delay := h.opts.reconnectMin
	for i := 0; i < attempt && delay < h.opts.reconnectMax; i++ {
		delay *= 2
	}
	delay = min(delay, h.opts.reconnectMax)
	// The jitter takes a random part of up to one half of the delay.
	delay = delay/2 + time.Duration(mathrand.Int64N(int64(delay/2)+1)) //nolint:gosec // jitter needs no secure random source
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		pollTimer := time.NewTimer(max(0, h.opts.pollInterval-time.Since(*lastPoll)))
		select {
		case <-ctx.Done():
			pollTimer.Stop()
			return false
		case <-timer.C:
			pollTimer.Stop()
			return true
		case <-pollTimer.C:
			h.poll(ctx)
			*lastPoll = time.Now()
		}
	}
}

// pollLoop polls every PollInterval until ctx ends. It runs when the hub has
// no LISTEN connection on purpose (a test setting).
func (h *Hub) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(h.opts.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.poll(ctx)
		}
	}
}

// notify handles the payload "<seq> <key>" of one notification. It marks a
// tracked feed dirty. A bad payload is logged at debug level and ignored.
func (h *Hub) notify(payload string) {
	key, seq, ok := parsePayload(payload)
	if !ok {
		h.log.Debug("live: a notification has a bad payload; the hub ignores it", "payload", payload)
		return
	}
	if h.tracked(key) == nil {
		return
	}
	h.dmu.Lock()
	h.notified[key] = max(h.notified[key], seq)
	h.dmu.Unlock()
	h.mark(key)
}

// parsePayload parses "<seq> <key>".
func parsePayload(payload string) (Key, int64, bool) {
	digits, rest, found := strings.Cut(payload, " ")
	if !found || !cursorDigits(digits) {
		return "", 0, false
	}
	seq, err := strconv.ParseInt(digits, 10, 64)
	key := Key(rest)
	if err != nil || key.Valid() != nil {
		return "", 0, false
	}
	return key, seq, true
}
