package livetest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/x/live"
)

// headerWait is the longest time that Open waits for the status of the handler.
const headerWait = 10 * time.Second

// errWriteTimeout is the read error of a stream whose handler passed its
// write deadline. A real client sees a closed connection in this case.
var errWriteTimeout = errors.New("livetest: the connection closed, because a write of the handler passed its deadline")

// closeWait is the longest time that Close waits for the handler to return.
const closeWait = 10 * time.Second

// Hub returns a started hub for a test. The defaults are WithPollInterval(50ms)
// and WithHeartbeat(100ms). opts come after the defaults, so they replace them.
// A cleanup closes the hub.
func Hub(t testing.TB, pool *pgxpool.Pool, opts ...live.Option) *live.Hub {
	t.Helper()
	base := []live.Option{live.WithPollInterval(50 * time.Millisecond), live.WithHeartbeat(100 * time.Millisecond)}
	h, err := live.New(pool, append(base, opts...)...)
	if err != nil {
		t.Fatalf("livetest: create the hub: %v", err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("livetest: start the hub: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), closeWait)
		defer cancel()
		if err := h.Close(ctx); err != nil {
			t.Errorf("livetest: close the hub: %v", err)
		}
	})
	return h
}

// RequestOption changes the request that Open sends.
type RequestOption func(*http.Request)

// WithHeader sets a request header.
func WithHeader(key, value string) RequestOption {
	return func(r *http.Request) { r.Header.Set(key, value) }
}

// WithUser puts u in the request context as the signed-in user, like authtest.AsUser.
func WithUser(u auth.User) RequestOption {
	return func(r *http.Request) { *r = *r.WithContext(auth.WithUser(r.Context(), u)) }
}

// Event is one server-sent event. A comment line, such as the heartbeat, is an
// event with the Name "" and the comment text in Data ("ping").
type Event struct {
	// Name is the value of the event field.
	Name string
	// ID is the value of the id field.
	ID string
	// Data holds the data lines, joined with "\n".
	Data string
}

// Stream is the response of a handler that runs in the test process. Its
// methods are for one goroutine.
type Stream struct {
	t      testing.TB
	w      *pipeWriter
	pr     *io.PipeReader
	cancel context.CancelFunc
	events chan result
	stop   chan struct{}
	done   chan struct{}
	err    error
	once   sync.Once
}

type result struct {
	event Event
	err   error
}

// Open runs h with a GET request to target and returns the stream. It returns
// when the handler writes its status, or when the handler returns. The
// response body goes through a pipe with no buffer, so a test that does not
// read the stream makes the writes of the handler wait, like a slow client.
// The request context ends at Close or at cleanup.
func Open(t testing.TB, h http.Handler, target string, opts ...RequestOption) *Stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	for _, opt := range opts {
		opt(r)
	}
	pr, pw := io.Pipe()
	s := &Stream{
		t:      t,
		w:      &pipeWriter{header: make(http.Header), pw: pw, wroteHeader: make(chan struct{})},
		pr:     pr,
		cancel: cancel,
		events: make(chan result),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go s.serve(h, r)
	go s.parse()
	t.Cleanup(s.Close)
	select {
	case <-s.w.wroteHeader:
	case <-s.done:
	case <-time.After(headerWait):
		s.Close()
		t.Fatalf("livetest: the handler wrote no status in %s", headerWait)
	}
	return s
}

// serve runs the handler. When it returns, the pipe closes, and the reader
// gets io.EOF after the last event.
func (s *Stream) serve(h http.Handler, r *http.Request) {
	defer close(s.done)
	var panicErr error
	func() {
		defer func() {
			if v := recover(); v != nil {
				panicErr = fmt.Errorf("livetest: the handler panicked: %v", v)
			}
		}()
		h.ServeHTTP(s.w, r)
	}()
	s.w.WriteHeader(http.StatusOK)
	_ = s.w.pw.CloseWithError(panicErr)
}

// parse reads events from the pipe and hands them to Next one at a time.
func (s *Stream) parse() {
	br := bufio.NewReader(s.pr)
	for {
		ev, err := readEvent(br)
		select {
		case s.events <- result{ev, err}:
		case <-s.stop:
			return
		}
		if err != nil {
			return
		}
	}
}

// readEvent reads one event. A comment line is an event of its own. A partial
// event at the end of the body is dropped.
func readEvent(br *bufio.Reader) (Event, error) {
	var ev Event
	var data []string
	fields := false
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return Event{}, err
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			if !fields {
				continue
			}
			ev.Data = strings.Join(data, "\n")
			return ev, nil
		}
		name, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		if name == "" {
			if fields {
				continue
			}
			return Event{Data: value}, nil
		}
		fields = true
		switch name {
		case "event":
			ev.Name = value
		case "id":
			ev.ID = value
		case "data":
			data = append(data, value)
		}
	}
}

// Status returns the status that the handler wrote, or 200 when the handler
// returned with no status.
func (s *Stream) Status() int {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	return s.w.status
}

// Header returns a copy of the response header at the time of the status.
func (s *Stream) Header() http.Header {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	return s.w.sent.Clone()
}

// Next returns the next event. It returns io.EOF when the handler returned and
// all events were read. When no event comes in timeout, it returns an error
// that wraps os.ErrDeadlineExceeded, and a later call can still read the event.
func (s *Stream) Next(timeout time.Duration) (Event, error) {
	if s.err != nil {
		return Event{}, s.err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-s.events:
		if r.err != nil {
			s.err = r.err
		}
		return r.event, r.err
	case <-timer.C:
		return Event{}, fmt.Errorf("livetest: no event in %s: %w", timeout, os.ErrDeadlineExceeded)
	}
}

// Close ends the request context, closes the body, and waits for the handler
// to return. It is safe to call more than once.
func (s *Stream) Close() {
	s.once.Do(func() {
		s.cancel()
		close(s.stop)
		_ = s.pr.CloseWithError(io.ErrClosedPipe)
		select {
		case <-s.done:
		case <-time.After(closeWait):
			s.t.Errorf("livetest: the handler did not return in %s after Close", closeWait)
		}
	})
}

// pipeWriter is an http.ResponseWriter that writes the body to a pipe. It
// implements http.Flusher and SetWriteDeadline, so http.ResponseController
// works with it.
type pipeWriter struct {
	header      http.Header
	pw          *io.PipeWriter
	mu          sync.Mutex
	status      int
	sent        http.Header
	deadline    time.Time
	wroteHeader chan struct{}
	expired     atomic.Bool
}

func (w *pipeWriter) Header() http.Header { return w.header }

func (w *pipeWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status != 0 {
		return
	}
	w.status = status
	w.sent = w.header.Clone()
	close(w.wroteHeader)
}

// Write writes to the pipe. It waits until the reader takes the bytes, or until
// the write deadline. A passed deadline closes the pipe, like a write timeout
// closes a connection, and Write returns os.ErrDeadlineExceeded.
func (w *pipeWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	w.mu.Lock()
	deadline := w.deadline
	w.mu.Unlock()
	if w.expired.Load() {
		return 0, os.ErrDeadlineExceeded
	}
	if !deadline.IsZero() {
		wait := time.Until(deadline)
		if wait <= 0 {
			w.expire()
			return 0, os.ErrDeadlineExceeded
		}
		timer := time.AfterFunc(wait, w.expire)
		defer timer.Stop()
	}
	n, err := w.pw.Write(p)
	if err != nil && w.expired.Load() {
		return n, os.ErrDeadlineExceeded
	}
	return n, err
}

func (w *pipeWriter) expire() {
	w.expired.Store(true)
	_ = w.pw.CloseWithError(errWriteTimeout)
}

// Flush writes the status when it is not written yet. The pipe has no buffer.
func (w *pipeWriter) Flush() { w.WriteHeader(http.StatusOK) }

// FlushError is Flush with an error result for http.ResponseController.
func (w *pipeWriter) FlushError() error {
	w.WriteHeader(http.StatusOK)
	if w.expired.Load() {
		return os.ErrDeadlineExceeded
	}
	return nil
}

// SetWriteDeadline sets the deadline of the next writes. The zero time clears it.
func (w *pipeWriter) SetWriteDeadline(t time.Time) error {
	if w.expired.Load() {
		return errors.New("livetest: the write deadline passed and the pipe is closed")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = t
	return nil
}
