package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

func readAll(t *testing.T, r io.Reader) ([]Event, int) {
	t.Helper()
	sse := NewSSEReader(r)
	var out []Event
	for {
		ev, err := sse.Next()
		if errors.Is(err, io.EOF) {
			return out, sse.Comments
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, ev)
	}
}

func TestSSEReaderSplitChunksCRLFAndComments(t *testing.T) {
	long := strings.Repeat("x", 10000)
	in := "event: ready\r\nid: 1-2\r\ndata: {\"epoch\":1,\"seq\":2}\r\n\r\n" +
		": ping\n\n" +
		"event: patch\nid: 1-3\ndata: a\ndata:b\n\n" +
		"retry: 5\nevent: patch\ndata: " + long + "\n\n" +
		"event: bye\ndata: {\"reason\":\"drain\"}\n\n" +
		"event: patch\ndata: never ends"
	for name, r := range map[string]io.Reader{
		"whole":    strings.NewReader(in),
		"one byte": iotest.OneByteReader(strings.NewReader(in)),
		"half":     iotest.HalfReader(strings.NewReader(in)),
	} {
		t.Run(name, func(t *testing.T) {
			events, comments := readAll(t, r)
			want := []Event{
				{Event: "ready", ID: "1-2", Data: `{"epoch":1,"seq":2}`},
				{Event: "patch", ID: "1-3", Data: "a\nb"},
				{Event: "patch", Data: long},
				{Event: "bye", Data: `{"reason":"drain"}`},
			}
			if len(events) != len(want) {
				t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
			}
			for i := range want {
				if events[i] != want[i] {
					t.Fatalf("event %d = %+v, want %+v", i, events[i], want[i])
				}
			}
			if comments != 1 {
				t.Fatalf("comments = %d, want 1", comments)
			}
		})
	}
}

func TestPercentiles(t *testing.T) {
	if got := percentiles(nil); got != (Latency{}) {
		t.Fatalf("percentiles(nil) = %+v, want zero", got)
	}
	xs := make([]time.Duration, 0, 100)
	for i := 100; i >= 1; i-- {
		xs = append(xs, time.Duration(i)*time.Millisecond)
	}
	got := percentiles(xs)
	want := Latency{P50: 50, P90: 90, P99: 99, Max: 100, Mean: 50.5}
	if got != want {
		t.Fatalf("percentiles = %+v, want %+v", got, want)
	}
	if one := percentiles([]time.Duration{3 * time.Millisecond}); one.P50 != 3 || one.P99 != 3 || one.Max != 3 {
		t.Fatalf("one sample = %+v", one)
	}
}

func TestBackoffAndRetryDelay(t *testing.T) {
	zero := func() float64 { return 0 }
	one := func() float64 { return 1 }
	if d := backoff(0, zero); d != 250*time.Millisecond {
		t.Fatalf("backoff(0, 0) = %v, want 250ms", d)
	}
	if d := backoff(2, one); d != 4*time.Second {
		t.Fatalf("backoff(2, 1) = %v, want 4s", d)
	}
	if d := backoff(20, one); d != 30*time.Second {
		t.Fatalf("backoff(20, 1) = %v, want 30s", d)
	}
	if d := retryDelay(0, 5*time.Second, zero); d != 5*time.Second {
		t.Fatalf("retryDelay with Retry-After 5 = %v, want 5s", d)
	}
	if d := retryDelay(0, 0, one); d != 4*time.Second {
		t.Fatalf("retryDelay(0, 0, 1) = %v, want 1s + 3s jitter", d)
	}
}

func TestParseFlags(t *testing.T) {
	cfg, err := parseFlags([]string{"-base", "http://127.0.0.1:9/"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Base != "http://127.0.0.1:9" || cfg.Feed != "notes" || cfg.Clients != 1000 || cfg.Patches != 50 ||
		cfg.PublishRate != 10 || cfg.PublishURL != "http://127.0.0.1:9/api/notes" || cfg.Resume || cfg.DrainPID != 0 {
		t.Fatalf("defaults = %+v", cfg)
	}
	cfg, err = parseFlags([]string{"-base", "http://h:1", "-feed", "memos", "-clients", "5", "-patches", "2",
		"-publish-url", "http://h:1/api/x", "-publish-body", `{"a":1}`, "-publish-rate", "50", "-resume",
		"-drain-pid", "7", "-server-pid", "8", "-cookie", "session=abc", "-timeout", "3s"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Feed != "memos" || cfg.Clients != 5 || cfg.Patches != 2 || cfg.PublishURL != "http://h:1/api/x" ||
		cfg.PublishBody != `{"a":1}` || cfg.PublishRate != 50 || !cfg.Resume || cfg.DrainPID != 7 ||
		cfg.ServerPID != 8 || cfg.Cookie != "session=abc" || cfg.Timeout != 3*time.Second {
		t.Fatalf("parsed = %+v", cfg)
	}
	for _, args := range [][]string{
		{},
		{"-base", "ftp://h"},
		{"-base", "http://h", "-clients", "0"},
		{"-base", "http://h", "-patches", "0"},
		{"-base", "http://h", "-publish-rate", "0"},
		{"-base", "http://h", "-timeout", "0s"},
		{"-base", "http://h", "-feed", "Notes"},
		{"-base", "http://h", "-publish-body", "{"},
		{"-base", "http://h", "-drain-pid", "-1"},
	} {
		if _, err := parseFlags(args, io.Discard); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("parseFlags(%q) = %v, want a usage error with a Try: line", args, err)
		}
	}
}

func TestCheckArrivals(t *testing.T) {
	t0 := time.Unix(100, 0)
	ends := []time.Time{t0, t0.Add(time.Second), t0.Add(2 * time.Second)}
	ids := []string{"a", "b", "c"}
	ok := checkArrivals(10, []arrival{
		{seq: 11, at: t0.Add(5 * time.Millisecond), change: "a"},
		{seq: 12, at: t0.Add(999 * time.Millisecond), change: "b"},
		{seq: 13, at: t0.Add(2*time.Second + time.Millisecond), change: "c"},
	}, ends, ids)
	if ok.missed != 0 || ok.duplicates != 0 || ok.outOfOrder != 0 || ok.mismatched != 0 || ok.early != 1 {
		t.Fatalf("clean stats = %+v", ok)
	}
	if len(ok.latencies) != 3 || ok.latencies[0] != 5*time.Millisecond || ok.latencies[1] != 0 {
		t.Fatalf("latencies = %v", ok.latencies)
	}
	bad := checkArrivals(10, []arrival{
		{seq: 11, at: t0, change: "a"},
		{seq: 11, at: t0, change: "a"},
		{seq: 13, at: t0, change: "x"},
	}, ends, ids)
	if bad.missed != 1 || bad.duplicates != 1 || bad.outOfOrder != 1 || bad.mismatched != 1 {
		t.Fatalf("bad stats = %+v", bad)
	}
}

// fakeFeed emulates the snapshot, the stream, and the POST of a live app (ADR 0033 section 7).
type fakeFeed struct {
	mu       sync.Mutex
	epoch    int64
	patches  []string // patch i has the sequence i+1
	subs     map[chan struct{}]struct{}
	refuse   atomic.Int64 // answer this many stream requests with no since with 503
	drainCh  chan struct{}
	streams  atomic.Int64
	sinceHit atomic.Int64
}

func newFakeFeed() *fakeFeed {
	return &fakeFeed{epoch: 1700000000000, subs: map[chan struct{}]struct{}{}, drainCh: make(chan struct{})}
}

func (f *fakeFeed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Cookie") != "session=abc" {
		http.Error(w, "no session", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/live/notes":
		f.mu.Lock()
		seq := len(f.patches)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"epoch":%d,"seq":%d,"rows":[]}`, f.epoch, seq)
	case r.Method == http.MethodPost && r.URL.Path == "/api/notes":
		f.mu.Lock()
		id := strconv.Itoa(len(f.patches) + 100)
		f.patches = append(f.patches, id)
		for ch := range f.subs {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":%s,"body":"x"}`, id)
	case r.Method == http.MethodGet && r.URL.Path == "/api/live/notes/live":
		f.stream(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeFeed) stream(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("since") == "" && f.refuse.Add(-1) >= 0 {
		w.Header().Set("Retry-After", "0")
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	f.streams.Add(1)
	defer f.streams.Add(-1)
	wake := make(chan struct{}, 1)
	f.mu.Lock()
	head := len(f.patches)
	next := head
	resumed := false
	if s := r.URL.Query().Get("since"); s != "" {
		c, ok := parseCursor(s)
		if !ok || (c.epoch != f.epoch && c != (cursor{})) || int(c.seq) > head {
			f.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: sync\ndata: {\"epoch\":%d,\"seq\":%d,\"reason\":\"epoch\"}\n\n", f.epoch, head)
			return
		}
		f.sinceHit.Add(1)
		next, resumed = int(c.seq), true
	}
	f.subs[wake] = struct{}{}
	f.mu.Unlock()
	defer func() { f.mu.Lock(); delete(f.subs, wake); f.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	_, _ = fmt.Fprintf(w, ": ping\n\nevent: ready\r\nid: %d-%d\r\ndata: {\"epoch\":%d,\"seq\":%d,\"resumed\":%t}\r\n\r\n",
		f.epoch, head, f.epoch, head, resumed)
	fl.Flush()
	send := func() {
		f.mu.Lock()
		ps := append([]string(nil), f.patches[next:]...)
		f.mu.Unlock()
		for _, id := range ps {
			next++
			_, _ = fmt.Fprintf(w, "event: patch\nid: %d-%d\ndata: {\"epoch\":%d,\"seq\":%d,\"changes\":[{\"op\":\"upsert\",\"id\":%q,\"row\":{}}]}\n\n",
				f.epoch, next, f.epoch, next, id)
		}
		fl.Flush()
	}
	send()
	for {
		select {
		case <-wake:
			send()
		case <-f.drainCh:
			_, _ = io.WriteString(w, "event: bye\ndata: {\"reason\":\"drain\"}\n\n")
			fl.Flush()
			return
		case <-r.Context().Done():
			return
		}
	}
}

func startFake(t *testing.T, f *fakeFeed) *httptest.Server {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("the sandbox cannot listen on loopback: %v", err)
	}
	srv := httptest.NewUnstartedServer(f)
	srv.Listener = listener
	srv.Start()
	t.Cleanup(func() {
		select {
		case <-f.drainCh:
		default:
			close(f.drainCh)
		}
		srv.Close()
	})
	return srv
}

func TestRunFanoutMemoryAndResume(t *testing.T) {
	randomFloat = func() float64 { return 0 }
	t.Cleanup(func() { randomFloat = rngDefault })
	f := newFakeFeed()
	srv := startFake(t, f)
	f.refuse.Store(3) // the first open attempts get 503 and retry
	cfg, err := parseFlags([]string{"-base", srv.URL, "-cookie", "session=abc", "-clients", "250", "-patches", "5",
		"-publish-rate", "200", "-resume", "-server-pid", strconv.Itoa(os.Getpid()), "-timeout", "20s"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	res := Run(context.Background(), cfg)
	if len(res.Failures) > 0 {
		t.Fatalf("failures: %v", res.Failures)
	}
	if !res.Conformance.OK {
		t.Fatalf("conformance = %+v", res.Conformance)
	}
	fan := res.Fanout
	if fan == nil || fan.Samples != 250*5 || fan.Missed != 0 || fan.Duplicates != 0 || !fan.OrderOK {
		t.Fatalf("fanout = %+v", fan)
	}
	if fan.OpenRetry != 3 {
		t.Fatalf("open retries = %d, want 3", fan.OpenRetry)
	}
	if fan.Latency.P50 > fan.Latency.P99 || fan.Latency.P99 > fan.Latency.Max {
		t.Fatalf("latency not ordered: %+v", fan.Latency)
	}
	if res.Memory == nil || res.Memory.BeforeKiB <= 0 || res.Memory.AfterKiB <= 0 || res.Memory.Error != "" {
		t.Fatalf("memory = %+v", res.Memory)
	}
	rs := res.Resume
	if rs == nil || rs.Resumed != 250 || rs.Sync != 0 || rs.Failed != 0 || rs.Status503 != 0 {
		t.Fatalf("resume = %+v", rs)
	}
	// conformance (1) + resume (250) use since.
	if got := f.sinceHit.Load(); got != 251 {
		t.Fatalf("since requests = %d, want 251", got)
	}
}

func TestRunResumeAfterSync(t *testing.T) {
	randomFloat = func() float64 { return 0 }
	t.Cleanup(func() { randomFloat = rngDefault })
	f := newFakeFeed()
	srv := startFake(t, f)
	cfg, err := parseFlags([]string{"-base", srv.URL, "-cookie", "session=abc", "-clients", "20", "-patches", "2",
		"-publish-rate", "200", "-resume", "-timeout", "10s"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(cfg)
	defer r.close()
	if err := r.conformance(context.Background()); err != nil {
		t.Fatal(err)
	}
	streams := make([]*stream, cfg.Clients)
	for i := range streams {
		streams[i] = &stream{run: r, client: r.httpClient(i)}
	}
	defer func() {
		for _, s := range streams {
			s.stop()
		}
	}()
	var st openStats
	if err := r.openAll(context.Background(), streams, func(*stream) string { return "" }, &st, false); err != nil {
		t.Fatal(err)
	}
	// A new epoch makes every resume cursor stale: each stream gets sync, then loads the snapshot.
	f.mu.Lock()
	f.epoch++
	f.mu.Unlock()
	rs := &Resume{Clients: cfg.Clients}
	if err := r.resume(context.Background(), streams, rs); err != nil {
		t.Fatalf("resume: %v (%+v)", err, rs)
	}
	if rs.Sync != 20 || rs.Resumed != 20 {
		t.Fatalf("resume = %+v, want 20 sync events, then 20 resumed streams", rs)
	}
}

func TestDrainCountsBye(t *testing.T) {
	f := newFakeFeed()
	srv := startFake(t, f)
	cfg, err := parseFlags([]string{"-base", srv.URL, "-cookie", "session=abc", "-clients", "10", "-timeout", "5s"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(cfg)
	defer r.close()
	streams := make([]*stream, cfg.Clients)
	for i := range streams {
		streams[i] = &stream{run: r, client: r.httpClient(i)}
	}
	var st openStats
	if err := r.openAll(context.Background(), streams, func(*stream) string { return "" }, &st, false); err != nil {
		t.Fatal(err)
	}
	close(f.drainCh)
	for _, s := range streams {
		<-s.done
	}
	n := 0
	for _, s := range streams {
		if s.endState() == "bye:drain" {
			n++
		}
	}
	if n != 10 {
		t.Fatalf("bye count = %d, want 10", n)
	}
}

func TestConformanceFails(t *testing.T) {
	f := newFakeFeed()
	srv := startFake(t, f)
	cfg, err := parseFlags([]string{"-base", srv.URL, "-cookie", "wrong", "-clients", "2", "-timeout", "2s"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	res := Run(context.Background(), cfg)
	if res.Conformance.OK || len(res.Failures) != 1 || res.Fanout != nil {
		t.Fatalf("result = %+v, want a failed conformance check and no scenario", res)
	}
}

var rngDefault = randomFloat
