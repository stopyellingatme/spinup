package live

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// The defaults of ADR 0033 section 4.
const (
	defaultMaxStreams        = 2000
	defaultMaxStreamsPerUser = 8
	defaultMaxStreamAge      = 15 * time.Minute
	defaultHeartbeat         = 15 * time.Second
	defaultPollInterval      = 5 * time.Second
	defaultRingPatches       = 1024
	defaultRingBytes         = 8 << 20
	defaultBootstrapLimit    = 4
	defaultBootstrapWait     = 2 * time.Second
	defaultRetention         = time.Hour
	defaultIdleTTL           = 60 * time.Second
	defaultWriteTimeout      = 10 * time.Second
	defaultSnapshotLimit     = 500

	// startListenTimeout limits the first LISTEN attempt of Start.
	startListenTimeout = 5 * time.Second
	// fetchWorkers is the number of goroutines that read new patches.
	fetchWorkers = 4
	// pruneFeedAge is the Feeds age of the Prune call of the janitor.
	pruneFeedAge = 30 * 24 * time.Hour
)

// hubState is the lifecycle state of a Hub.
type hubState int

const (
	stateNew hubState = iota
	stateStarted
	stateDraining
	stateClosed
)

// The errors of the unexported contract of ADR 0033 section 6.12. The stream
// code of unit LV4 maps them to the answers of section 7.5.
var (
	// errBusy means that no bootstrap slot became free in BootstrapWait.
	errBusy = errors.New("live: no bootstrap slot is free")
	// errUserLimit means that the user has MaxStreamsPerUser open streams.
	errUserLimit = errors.New("live: the user has the most streams that the hub allows")
	// errGlobalLimit means that the process has MaxStreams open streams.
	errGlobalLimit = errors.New("live: the process has the most streams that the hub allows")
	// errHubClosed means that Close ran.
	errHubClosed = errors.New("live: the hub is closed")
)

// Hub holds the streams of one process.
type Hub struct {
	pool *pgxpool.Pool
	opts options
	m    *metrics
	log  *slog.Logger

	// mu guards feeds, ringBytes, streams, perUser, state, cancel, and the
	// refs and idleSince fields of each feed. Every change to a ring happens
	// while mu is held, so ringBytes is exact. The lock order is mu, then
	// feed.mu, then dmu.
	mu        sync.Mutex
	feeds     map[Key]*feed
	ringBytes int64
	streams   int
	perUser   map[int64]int
	state     hubState
	cancel    context.CancelFunc

	// boot holds the bootstrap slots (section 6.9).
	boot chan struct{}
	// drainCh is closed by Drain (unit LV4).
	drainCh chan struct{}
	// wg counts the open streams (unit LV4).
	wg sync.WaitGroup

	// The dirty set of section 6.6. dmu guards pending, fetchState, and notified.
	dmu        sync.Mutex
	pending    []Key
	fetchState map[Key]fetchMark
	notified   map[Key]int64
	work       chan struct{}

	// loops counts the listener, the fetch workers, and the janitor.
	loops      sync.WaitGroup
	listenerUp atomic.Bool
	listenPID  atomic.Uint32
}

// Option changes a setting of a Hub.
type Option func(*options)

type options struct {
	logger            *slog.Logger
	meterProvider     metric.MeterProvider
	tracerProvider    trace.TracerProvider
	maxStreams        int
	maxStreamsPerUser int
	maxStreamAge      time.Duration
	heartbeat         time.Duration
	pollInterval      time.Duration
	ringPatches       int
	ringBytes         int64
	bootstrapLimit    int
	bootstrapWait     time.Duration
	retention         time.Duration
	idleTTL           time.Duration
	writeTimeout      time.Duration
	snapshotLimit     int

	// The settings below have no exported option. Tests change them.
	noListen     bool          // poll only, with no LISTEN connection
	reconnectMin time.Duration // the first reconnect delay; default 1 s
	reconnectMax time.Duration // the longest reconnect delay; default 30 s
	pruneEvery   time.Duration // the time between two Prune calls; default 5 min
	ringCheck    time.Duration // the time between two ring byte checks; default 15 s
	beforeDial   func()        // runs before each reconnect attempt
	duringReset  func(*feed)   // runs in applyReset while it holds the feed lock
}

// New validates the options and the pool. It does no I/O. A limit of zero or
// less uses the default. A negative duration is an error.
func New(pool *pgxpool.Pool, opts ...Option) (*Hub, error) {
	if pool == nil {
		return nil, &Error{Op: "create hub", Err: errors.New("the pool is nil"), Try: "pass the pool of the app, such as the pool from db.Open"}
	}
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if err := o.resolve(); err != nil {
		return nil, err
	}
	m, err := newMetrics(o.meterProvider, o.tracerProvider)
	if err != nil {
		return nil, err
	}
	return &Hub{
		pool:       pool,
		opts:       o,
		m:          m,
		log:        o.logger,
		feeds:      make(map[Key]*feed),
		perUser:    make(map[int64]int),
		boot:       make(chan struct{}, o.bootstrapLimit),
		drainCh:    make(chan struct{}),
		fetchState: make(map[Key]fetchMark),
		notified:   make(map[Key]int64),
		work:       make(chan struct{}, 1),
	}, nil
}

// resolve checks the durations and puts the defaults in place of zero values.
func (o *options) resolve() error {
	durations := []struct {
		name  string
		value *time.Duration
		def   time.Duration
	}{
		{"WithMaxStreamAge", &o.maxStreamAge, defaultMaxStreamAge},
		{"WithHeartbeat", &o.heartbeat, defaultHeartbeat},
		{"WithPollInterval", &o.pollInterval, defaultPollInterval},
		{"WithBootstrapWait", &o.bootstrapWait, defaultBootstrapWait},
		{"WithRetention", &o.retention, defaultRetention},
		{"WithIdleTTL", &o.idleTTL, defaultIdleTTL},
		{"WithWriteTimeout", &o.writeTimeout, defaultWriteTimeout},
		{"reconnect minimum", &o.reconnectMin, time.Second},
		{"reconnect maximum", &o.reconnectMax, 30 * time.Second},
		{"prune interval", &o.pruneEvery, 5 * time.Minute},
		{"ring check interval", &o.ringCheck, 15 * time.Second},
	}
	for _, d := range durations {
		if *d.value < 0 {
			return &Error{Op: "create hub", Err: fmt.Errorf("%s has the negative duration %s", d.name, *d.value), Try: "pass a positive duration, or 0 for the default"}
		}
		if *d.value == 0 {
			*d.value = d.def
		}
	}
	o.reconnectMax = max(o.reconnectMax, o.reconnectMin)
	positive := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	positive(&o.maxStreams, defaultMaxStreams)
	positive(&o.maxStreamsPerUser, defaultMaxStreamsPerUser)
	positive(&o.ringPatches, defaultRingPatches)
	positive(&o.bootstrapLimit, defaultBootstrapLimit)
	positive(&o.snapshotLimit, defaultSnapshotLimit)
	if o.ringBytes <= 0 {
		o.ringBytes = defaultRingBytes
	}
	if o.logger == nil {
		o.logger = slog.New(lateDefault{})
	}
	return nil
}

// Start opens the LISTEN connection and starts the loops: the listener, the
// fetch workers, and the janitor. ctx ends the loops. The first LISTEN attempt
// runs before Start returns, with a limit of 5 seconds. A failed attempt logs a
// warning, and the listener keeps trying. A second call returns an error.
func (h *Hub) Start(ctx context.Context) error {
	h.mu.Lock()
	switch h.state {
	case stateNew:
	case stateClosed:
		h.mu.Unlock()
		return &Error{Op: "start hub", Err: errHubClosed, Try: "create a new hub with live.New"}
	default:
		h.mu.Unlock()
		return &Error{Op: "start hub", Err: errors.New("the hub has already started"), Try: "call Start only once for each hub"}
	}
	h.state = stateStarted
	runCtx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	h.mu.Unlock()

	h.m.observe(h.listenerUp.Load, h.totalRingBytes)
	var conn *pgx.Conn
	if !h.opts.noListen {
		firstCtx, firstCancel := context.WithTimeout(runCtx, startListenTimeout)
		c, err := h.connectListener(firstCtx)
		firstCancel()
		if err != nil {
			h.log.WarnContext(ctx, "live: cannot open the LISTEN connection; the hub polls and tries again", "error", err)
		} else {
			conn = c
		}
	}
	h.loops.Add(2 + fetchWorkers)
	go h.listenLoop(runCtx, conn)
	for range fetchWorkers {
		go h.fetchWorker(runCtx)
	}
	go h.janitor(runCtx)
	return nil
}

// Ready returns an error before Start and after Drain begins. Otherwise it
// returns nil. A lost LISTEN connection is not a readiness failure, because
// the poll covers it.
func (h *Hub) Ready(_ context.Context) error {
	h.mu.Lock()
	state := h.state
	h.mu.Unlock()
	switch state {
	case stateStarted:
		return nil
	case stateNew:
		return &Error{Op: "check readiness", Err: errors.New("the hub has not started"), Try: "call Start before the server accepts requests"}
	case stateDraining:
		return &Error{Op: "check readiness", Err: errors.New("the hub is draining"), Try: "wait for the new instance of the server"}
	default:
		return &Error{Op: "check readiness", Err: errHubClosed, Try: "wait for the new instance of the server"}
	}
}

// Close stops the loops, closes the LISTEN connection, and waits for the
// workers, subject to ctx. Each tracked feed closes, so its streams end. Close
// also removes the gauge callback from the meter provider, so the provider no
// longer holds the hub. Close is safe without Start and when called twice.
func (h *Hub) Close(ctx context.Context) error {
	h.mu.Lock()
	if h.state == stateClosed {
		h.mu.Unlock()
		return nil
	}
	h.state = stateClosed
	cancel := h.cancel
	for _, f := range h.feeds {
		f.close()
	}
	h.mu.Unlock()
	h.m.unobserve()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() {
		h.loops.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return &Error{Op: "close hub", Err: ctx.Err(), Try: "allow more time for shutdown"}
	}
}

// acquire finds the feed of key, or loads it (section 6.5), and counts one
// reference. Other callers for the same feed wait for the load. A failed load
// removes the feed, so the next call loads it again.
func (h *Hub) acquire(ctx context.Context, key Key) (*feed, error) {
	for {
		h.mu.Lock()
		if h.state == stateClosed {
			h.mu.Unlock()
			return nil, errHubClosed
		}
		f, found := h.feeds[key]
		if !found {
			f = newFeed(key, h.opts.ringPatches)
			h.feeds[key] = f
		}
		f.refs++
		h.mu.Unlock()

		if !found {
			err := h.load(ctx, f)
			if err != nil {
				f.loadErr = err
				h.mu.Lock()
				if h.feeds[key] == f {
					delete(h.feeds, key)
				}
				f.refs--
				h.mu.Unlock()
				close(f.ready)
				return nil, err
			}
			close(f.ready)
			// A notification that came during the load found the feed not ready.
			h.mark(key)
			return f, nil
		}

		select {
		case <-f.ready:
		case <-ctx.Done():
			h.release(f)
			return nil, ctx.Err()
		}
		if f.loadErr == nil {
			return f, nil
		}
		h.mu.Lock()
		f.refs--
		h.mu.Unlock()
		// The request that loaded the feed went away. This caller loads it again.
		if errors.Is(f.loadErr, context.Canceled) && ctx.Err() == nil {
			continue
		}
		return nil, f.loadErr
	}
}

// release drops one reference of f. A feed with no reference becomes idle,
// and the janitor ends it after IdleTTL.
func (h *Hub) release(f *feed) {
	if f == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if f.refs <= 0 {
		return
	}
	f.refs--
	if f.refs == 0 {
		f.idleSince = time.Now()
	}
}

// slot takes a bootstrap slot (section 6.9). It waits at most BootstrapWait,
// then returns errBusy. The release function is safe to call twice.
func (h *Hub) slot(ctx context.Context) (release func(), err error) {
	select {
	case h.boot <- struct{}{}:
		return h.slotRelease(), nil
	default:
	}
	timer := time.NewTimer(h.opts.bootstrapWait)
	defer timer.Stop()
	select {
	case h.boot <- struct{}{}:
		return h.slotRelease(), nil
	case <-timer.C:
		h.m.bootstrapRejected()
		return nil, errBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *Hub) slotRelease() func() {
	var once sync.Once
	return func() { once.Do(func() { <-h.boot }) }
}

// admit counts one stream of the user (section 7.5). It returns errUserLimit
// when the user has MaxStreamsPerUser streams, and errGlobalLimit when the
// process has MaxStreams streams. The release function is safe to call twice.
func (h *Hub) admit(userID int64) (release func(), err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.perUser[userID] >= h.opts.maxStreamsPerUser {
		return nil, errUserLimit
	}
	if h.streams >= h.opts.maxStreams {
		return nil, errGlobalLimit
	}
	h.streams++
	h.perUser[userID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.streams--
			if h.perUser[userID]--; h.perUser[userID] <= 0 {
				delete(h.perUser, userID)
			}
		})
	}, nil
}

// readCursor reads the position of the feed on the pool (section 7.3, step 3).
// A feed with no row gives 0-0.
func (h *Hub) readCursor(ctx context.Context, key Key) (Cursor, error) {
	var c Cursor
	err := h.pool.QueryRow(ctx, `SELECT epoch, seq FROM live_feeds WHERE key = $1`, string(key)).Scan(&c.Epoch, &c.Seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return Cursor{}, nil
	}
	if err != nil {
		return Cursor{}, dbError("read the feed cursor", err)
	}
	return c, nil
}

// tracked returns the feed of key when the hub tracks it and its load ended.
func (h *Hub) tracked(key Key) *feed {
	h.mu.Lock()
	f := h.feeds[key]
	h.mu.Unlock()
	if f == nil || !isReady(f) {
		return nil
	}
	return f
}

func isReady(f *feed) bool {
	select {
	case <-f.ready:
		return f.loadErr == nil
	default:
		return false
	}
}

// totalRingBytes returns the sum of the bytes of all rings.
func (h *Hub) totalRingBytes() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ringBytes
}

// WithLogger sets the logger. The default is slog.Default(), read at each log call. An app builds
// the hub before app.New installs its logger, and the hub must use that logger.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithMeterProvider sets the meter provider. The default is otel.GetMeterProvider().
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *options) { o.meterProvider = mp }
}

// WithTracerProvider sets the tracer provider. The default is otel.GetTracerProvider().
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *options) { o.tracerProvider = tp }
}

// WithMaxStreams sets the most streams of the process. The default is 2000.
func WithMaxStreams(n int) Option { return func(o *options) { o.maxStreams = n } }

// WithMaxStreamsPerUser sets the most streams of one user. The default is 8.
func WithMaxStreamsPerUser(n int) Option { return func(o *options) { o.maxStreamsPerUser = n } }

// WithMaxStreamAge sets the longest age of a stream. The default is 15 minutes.
func WithMaxStreamAge(d time.Duration) Option { return func(o *options) { o.maxStreamAge = d } }

// WithHeartbeat sets the time between two pings of an idle stream. The default is 15 seconds.
func WithHeartbeat(d time.Duration) Option { return func(o *options) { o.heartbeat = d } }

// WithPollInterval sets the time between two polls. The default is 5 seconds.
func WithPollInterval(d time.Duration) Option { return func(o *options) { o.pollInterval = d } }

// WithRing sets the most patches of one ring and the most bytes of all rings.
// The defaults are 1024 and 8 MiB.
func WithRing(patches int, bytes int64) Option {
	return func(o *options) { o.ringPatches, o.ringBytes = patches, bytes }
}

// WithBootstrapLimit sets the number of bootstrap slots. The default is 4.
func WithBootstrapLimit(n int) Option { return func(o *options) { o.bootstrapLimit = n } }

// WithBootstrapWait sets the longest wait for a bootstrap slot. The default is 2 seconds.
func WithBootstrapWait(d time.Duration) Option { return func(o *options) { o.bootstrapWait = d } }

// WithRetention sets the patch age that the hub prunes. The default is 1 hour.
func WithRetention(d time.Duration) Option { return func(o *options) { o.retention = d } }

// WithIdleTTL sets how long the hub keeps a feed with no stream. The default is 60 seconds.
func WithIdleTTL(d time.Duration) Option { return func(o *options) { o.idleTTL = d } }

// WithWriteTimeout sets the write timeout of a stream. The default is 10 seconds.
func WithWriteTimeout(d time.Duration) Option { return func(o *options) { o.writeTimeout = d } }

// WithSnapshotLimit sets the most rows of a snapshot. The default is 500.
func WithSnapshotLimit(n int) Option { return func(o *options) { o.snapshotLimit = n } }
