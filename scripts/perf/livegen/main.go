// Command livegen is the live data load tool of the spinup performance loop (scripts/perf).
//
// It opens many server-sent event streams on one live feed of a generated app (ADR 0033
// section 17.6) and measures the scenarios below. It writes one JSON document to standard
// output with one object for each scenario that ran:
//
//   - conformance: one snapshot, one stream, and one POST. The patch of the POST must arrive.
//     This check runs before any timing. If it fails, no scenario runs.
//   - fanout: open -clients streams, wait for ready on all, then send -patches POST requests
//     (one at a time, at -publish-rate). For each patch and client, the latency is the time
//     from the end of the POST to the arrival of the patch frame. The object has the
//     percentiles, the missed and duplicate counts, and the sequence order check.
//   - memory (with -server-pid): the RSS of the server before the streams and after ready on
//     all, and the difference divided by the client count, in KiB.
//   - resume (with -resume): close all streams at one time, then open them again at one time
//     with since. The object has the time until all are ready, the sync count, the 503 and
//     429 counts, and the retries.
//   - drain (with -drain-pid): send SIGTERM to the process, then measure the time until all
//     streams ended, the bye count, and the time until the process exited.
//
// The exit code is 0 when every scenario passed, 1 when a scenario failed (the JSON is still
// written), and 2 for a usage error. The tool uses only the Go standard library.
//
// Example:
//
//	go run ./scripts/perf/livegen -base http://127.0.0.1:8080 -cookie 'session=abc' -clients 100 \
//	    -patches 10 -server-pid 4242 -resume -drain-pid 4242
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// clientsPerTransport is the number of stream clients that share one http.Transport.
const clientsPerTransport = 200

// maxAttempts is the most connect attempts of one stream in one scenario.
const maxAttempts = 12

// randomFloat is the random source of the retry jitter. Tests replace it.
var randomFloat = rand.Float64

// Config is the parsed command line of one run.
type Config struct {
	Base        string
	Feed        string
	Cookie      string
	Clients     int
	Patches     int
	PublishURL  string
	PublishBody string
	PublishRate float64
	Resume      bool
	DrainPID    int
	ServerPID   int
	Timeout     time.Duration
}

// Latency holds latency percentiles in milliseconds (nearest rank).
type Latency struct {
	P50  float64 `json:"p50"`
	P90  float64 `json:"p90"`
	P99  float64 `json:"p99"`
	Max  float64 `json:"max"`
	Mean float64 `json:"mean"`
}

// Conformance is the result of the check that runs before the timed scenarios.
type Conformance struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Fanout is the result of the fan-out scenario.
type Fanout struct {
	Clients    int     `json:"clients"`
	Patches    int     `json:"patches"`
	OpenS      float64 `json:"open_s"`
	OpenRetry  int64   `json:"open_retries"`
	Latency    Latency `json:"latency_ms"`
	Samples    int     `json:"samples"`
	Early      int     `json:"early"`
	Missed     int     `json:"missed"`
	Duplicates int     `json:"duplicates"`
	OutOfOrder int     `json:"out_of_order"`
	Mismatched int     `json:"mismatched"`
	OrderOK    bool    `json:"order_ok"`
	PublishMS  Latency `json:"publish_ms"`
	Error      string  `json:"error,omitempty"`
}

// Memory is the result of the memory scenario.
type Memory struct {
	BeforeKiB    int64   `json:"before_kib"`
	AfterKiB     int64   `json:"after_kib"`
	PerStreamKiB float64 `json:"per_stream_kib"`
	Error        string  `json:"error,omitempty"`
}

// Resume is the result of the resume-storm scenario.
type Resume struct {
	Clients     int     `json:"clients"`
	ReadyS      float64 `json:"ready_s"`
	Resumed     int64   `json:"resumed"`
	Sync        int64   `json:"sync"`
	Status503   int64   `json:"status_503"`
	Status429   int64   `json:"status_429"`
	Retries     int64   `json:"retries"`
	MaxAttempts int64   `json:"max_attempts"`
	Failed      int64   `json:"failed"`
	Error       string  `json:"error,omitempty"`
}

// Drain is the result of the drain scenario.
type Drain struct {
	Clients int     `json:"clients"`
	EndedS  float64 `json:"ended_s"`
	Bye     int     `json:"bye"`
	NoBye   int     `json:"no_bye"`
	Open    int     `json:"still_open"`
	ExitS   float64 `json:"exit_s"`
	Exited  bool    `json:"exited"`
	Error   string  `json:"error,omitempty"`
}

// Result is the JSON output of one run.
type Result struct {
	Base        string      `json:"base"`
	Feed        string      `json:"feed"`
	Clients     int         `json:"clients"`
	Patches     int         `json:"patches"`
	Conformance Conformance `json:"conformance"`
	Fanout      *Fanout     `json:"fanout,omitempty"`
	Memory      *Memory     `json:"memory,omitempty"`
	Resume      *Resume     `json:"resume,omitempty"`
	Drain       *Drain      `json:"drain,omitempty"`
	Failures    []string    `json:"failures"`
}

func main() {
	cfg, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	res := Run(context.Background(), cfg)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		fmt.Fprintf(os.Stderr, "livegen: cannot write the result: %v\nTry: check standard output\n", err)
		os.Exit(1)
	}
	if len(res.Failures) > 0 {
		fmt.Fprintf(os.Stderr, "livegen: %d scenario checks failed (first: %s)\nTry: read the app log and the JSON result\n",
			len(res.Failures), res.Failures[0])
		os.Exit(1)
	}
}

// parseFlags reads the command line. It returns a usage error with a Try: line.
func parseFlags(args []string, out io.Writer) (Config, error) {
	fs := flag.NewFlagSet("livegen", flag.ContinueOnError)
	fs.SetOutput(out)
	var cfg Config
	fs.StringVar(&cfg.Base, "base", "", "the base URL of the API, such as http://127.0.0.1:8080 (required)")
	fs.StringVar(&cfg.Feed, "feed", "notes", "the feed name")
	fs.StringVar(&cfg.Cookie, "cookie", "", "the value of a Cookie header for every request")
	fs.IntVar(&cfg.Clients, "clients", 1000, "the number of streams")
	fs.IntVar(&cfg.Patches, "patches", 50, "the number of POST requests (one patch each) in the fan-out scenario")
	fs.StringVar(&cfg.PublishURL, "publish-url", "", "the URL of the POST that publishes one patch (default <base>/api/<feed>)")
	fs.StringVar(&cfg.PublishBody, "publish-body", `{"body":"livegen"}`, "the JSON body of the POST")
	fs.Float64Var(&cfg.PublishRate, "publish-rate", 10, "the POST requests per second")
	fs.BoolVar(&cfg.Resume, "resume", false, "run the resume-storm scenario")
	fs.IntVar(&cfg.DrainPID, "drain-pid", 0, "run the drain scenario: send SIGTERM to this process")
	fs.IntVar(&cfg.ServerPID, "server-pid", 0, "run the memory scenario: read the RSS of this process")
	fs.DurationVar(&cfg.Timeout, "timeout", 60*time.Second, "the longest wait of one step")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	switch {
	case cfg.Base == "":
		return cfg, errors.New("livegen: -base is missing\nTry: livegen -base http://127.0.0.1:8080 -cookie 'session=abc'")
	case cfg.Clients < 1:
		return cfg, fmt.Errorf("livegen: -clients is %d, and it must be 1 or more\nTry: -clients 1000", cfg.Clients)
	case cfg.Patches < 1:
		return cfg, fmt.Errorf("livegen: -patches is %d, and it must be 1 or more\nTry: -patches 50", cfg.Patches)
	case cfg.PublishRate <= 0:
		return cfg, fmt.Errorf("livegen: -publish-rate is %v, and it must be more than 0\nTry: -publish-rate 10", cfg.PublishRate)
	case cfg.Timeout <= 0:
		return cfg, errors.New("livegen: -timeout must be more than 0\nTry: -timeout 60s")
	case cfg.DrainPID < 0 || cfg.ServerPID < 0:
		return cfg, errors.New("livegen: -drain-pid and -server-pid cannot be negative\nTry: pass the process ID of the API")
	case !validFeed(cfg.Feed):
		return cfg, fmt.Errorf("livegen: -feed %q is not a feed name\nTry: -feed notes", cfg.Feed)
	}
	u, err := url.Parse(cfg.Base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return cfg, fmt.Errorf("livegen: -base %q is not an http or https URL\nTry: -base http://127.0.0.1:8080", cfg.Base)
	}
	cfg.Base = strings.TrimRight(cfg.Base, "/")
	if cfg.PublishURL == "" {
		cfg.PublishURL = cfg.Base + "/api/" + cfg.Feed
	}
	if !json.Valid([]byte(cfg.PublishBody)) {
		return cfg, fmt.Errorf("livegen: -publish-body %q is not JSON\nTry: -publish-body '{\"body\":\"x\"}'", cfg.PublishBody)
	}
	return cfg, nil
}

// validFeed reports whether s is a feed name of ADR 0033 section 4 (without a scope).
func validFeed(s string) bool {
	if s == "" || len(s) > 48 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range []byte(s) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// The SSE parser
// ---------------------------------------------------------------------------

// Event is one server-sent event.
type Event struct {
	Event string
	ID    string
	Data  string
}

// SSEReader reads server-sent events from a stream. Lines end with "\n" or "\r\n".
// Comment lines (they start with ":") are not events: Comments counts them.
type SSEReader struct {
	r        *bufio.Reader
	line     []byte
	Comments int
}

// NewSSEReader returns a reader of the events in r.
func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{r: bufio.NewReaderSize(r, 4096)}
}

// readLine returns the next line without its line end. The slice is valid until the next call.
func (s *SSEReader) readLine() ([]byte, error) {
	s.line = s.line[:0]
	for {
		chunk, err := s.r.ReadSlice('\n')
		s.line = append(s.line, chunk...)
		switch {
		case err == nil:
			line := s.line[:len(s.line)-1]
			return bytes.TrimSuffix(line, []byte{'\r'}), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

// Next returns the next event. It returns io.EOF at the end of the stream. An event that the
// stream did not end with a blank line is dropped, as the SSE rules say.
func (s *SSEReader) Next() (Event, error) {
	var ev Event
	var data strings.Builder
	seen, hasData := false, false
	for {
		line, err := s.readLine()
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				err = io.EOF
			}
			return Event{}, err
		}
		if len(line) == 0 {
			if !seen {
				continue
			}
			ev.Data = data.String()
			return ev, nil
		}
		if line[0] == ':' {
			s.Comments++
			continue
		}
		field, value, found := bytes.Cut(line, []byte{':'})
		if found {
			value = bytes.TrimPrefix(value, []byte{' '})
		}
		switch string(field) {
		case "event":
			ev.Event = string(value)
		case "id":
			ev.ID = string(value)
		case "data":
			if hasData {
				data.WriteByte('\n')
			}
			data.Write(value)
			hasData = true
		default:
			continue // retry and unknown fields
		}
		seen = true
	}
}

// ---------------------------------------------------------------------------
// Statistics
// ---------------------------------------------------------------------------

// percentiles returns the nearest-rank percentiles of xs in milliseconds. It sorts xs.
func percentiles(xs []time.Duration) Latency {
	if len(xs) == 0 {
		return Latency{}
	}
	slices.Sort(xs)
	var sum time.Duration
	for _, x := range xs {
		sum += x
	}
	return Latency{
		P50:  ms(rank(xs, 0.50)),
		P90:  ms(rank(xs, 0.90)),
		P99:  ms(rank(xs, 0.99)),
		Max:  ms(xs[len(xs)-1]),
		Mean: ms(sum / time.Duration(len(xs))),
	}
}

// rank returns the nearest-rank percentile p (0 < p <= 1) of the sorted xs.
func rank(sorted []time.Duration, p float64) time.Duration {
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	i = max(0, min(i, len(sorted)-1))
	return sorted[i]
}

func ms(d time.Duration) float64 { return round(float64(d)/float64(time.Millisecond), 3) }

func seconds(d time.Duration) float64 { return round(d.Seconds(), 3) }

func round(x float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(x*p) / p
}

// backoff is the reconnect delay of the web helper (ADR 0033 section 12.6): a random part of
// min(30 s, 1 s * 2^attempt), at least 250 ms.
func backoff(attempt int, random func() float64) time.Duration {
	capMS := math.Min(30000, 1000*math.Pow(2, float64(attempt)))
	return time.Duration(math.Max(250, random()*capMS)) * time.Millisecond
}

// retryDelay is the wait before the next attempt after a 503 or 429 answer, or a network
// error: the larger of Retry-After and the backoff, plus a jitter of 0 to 3 s.
func retryDelay(attempt int, retryAfter time.Duration, random func() float64) time.Duration {
	d := max(retryAfter, backoff(attempt, random))
	return d + time.Duration(random()*3000)*time.Millisecond
}

// ---------------------------------------------------------------------------
// Streams
// ---------------------------------------------------------------------------

// cursor is a feed position.
type cursor struct{ epoch, seq int64 }

func (c cursor) String() string {
	return strconv.FormatInt(c.epoch, 10) + "-" + strconv.FormatInt(c.seq, 10)
}

// parseCursor reads "<epoch>-<seq>".
func parseCursor(s string) (cursor, bool) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return cursor{}, false
	}
	e, err1 := strconv.ParseInt(a, 10, 64)
	q, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || e < 0 || q < 0 {
		return cursor{}, false
	}
	return cursor{e, q}, true
}

// arrival is one patch frame that a stream received.
type arrival struct {
	seq    int64
	at     time.Time
	change string // the ID of the first change
}

// stream is one SSE client.
type stream struct {
	run    *runner
	client *http.Client

	mu       sync.Mutex
	ready    bool
	resumed  bool
	head     cursor
	last     cursor
	arrivals []arrival
	end      string // "bye:<reason>", "sync:<reason>", "eof", or "error: ..."
	endedAt  time.Time
	cancel   context.CancelFunc
	done     chan struct{}
	settle   func() // marks the connection as ready or ended, once
}

// openResult is the outcome of one connect attempt.
type openResult struct {
	status     int
	retryAfter time.Duration
	err        error
}

// runner holds the shared state of one run.
type runner struct {
	cfg        Config
	transports []*http.Transport
	random     func() float64
	patches    atomic.Int64 // patch frames received by all streams
	syncs      atomic.Int64
}

func newRunner(cfg Config) *runner {
	n := (cfg.Clients + clientsPerTransport - 1) / clientsPerTransport
	r := &runner{cfg: cfg, random: randomFloat}
	var h1 http.Protocols
	h1.SetHTTP1(true)
	for range n + 1 { // one more for the conformance stream and the POST requests
		r.transports = append(r.transports, &http.Transport{
			Protocols:             &h1,
			DialContext:           (&net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          clientsPerTransport,
			MaxIdleConnsPerHost:   clientsPerTransport,
			MaxConnsPerHost:       clientsPerTransport * 2,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: cfg.Timeout,
			DisableCompression:    true,
		})
	}
	return r
}

func (r *runner) close() {
	for _, t := range r.transports {
		t.CloseIdleConnections()
	}
}

// httpClient returns the client of stream i. The last transport serves the POST requests.
func (r *runner) httpClient(i int) *http.Client {
	t := r.transports[len(r.transports)-1]
	if i >= 0 {
		t = r.transports[i/clientsPerTransport]
	}
	return &http.Client{Transport: t}
}

// connect sends one stream request. On 200 it starts the reader goroutine of s. The reader
// calls settled.Done once: at the ready event, or when the stream ends before ready.
func (s *stream) connect(ctx context.Context, since string, settled *sync.WaitGroup) openResult {
	u := s.run.cfg.Base + "/api/live/" + s.run.cfg.Feed + "/live"
	if since != "" {
		u += "?since=" + url.QueryEscape(since)
	}
	sctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(sctx, http.MethodGet, u, nil)
	if err != nil {
		cancel()
		return openResult{err: err}
	}
	req.Header.Set("Accept", "text/event-stream")
	if s.run.cfg.Cookie != "" {
		req.Header.Set("Cookie", s.run.cfg.Cookie)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		cancel()
		return openResult{err: err}
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		cancel()
		ra, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return openResult{status: resp.StatusCode, retryAfter: time.Duration(ra) * time.Second}
	}
	s.mu.Lock()
	s.ready, s.resumed, s.end, s.endedAt = false, false, "", time.Time{}
	s.cancel = cancel
	s.done = make(chan struct{})
	s.settle = sync.OnceFunc(settled.Done)
	done := s.done
	s.mu.Unlock()
	go s.read(resp.Body, done)
	return openResult{status: http.StatusOK}
}

// read handles the events of one connection until it ends.
func (s *stream) read(body io.ReadCloser, done chan struct{}) {
	defer close(done)
	defer func() { _ = body.Close() }()
	sse := NewSSEReader(body)
	for {
		ev, err := sse.Next()
		now := time.Now()
		if err != nil {
			s.finish(now, "eof", err)
			return
		}
		switch ev.Event {
		case "ready":
			c, _ := parseCursor(ev.ID)
			var d struct {
				Resumed bool `json:"resumed"`
			}
			_ = json.Unmarshal([]byte(ev.Data), &d)
			s.mu.Lock()
			s.ready, s.resumed, s.head, s.last = true, d.Resumed, c, c
			settle := s.settle
			s.mu.Unlock()
			settle()
		case "patch":
			c, _ := parseCursor(ev.ID)
			s.mu.Lock()
			s.arrivals = append(s.arrivals, arrival{seq: c.seq, at: now, change: changeID(ev.Data)})
			s.last = c
			s.mu.Unlock()
			s.run.patches.Add(1)
		case "sync":
			s.run.syncs.Add(1)
			s.finish(now, "sync:"+reasonOf(ev.Data), nil)
			return
		case "bye":
			s.finish(now, "bye:"+reasonOf(ev.Data), nil)
			return
		}
	}
}

func (s *stream) finish(at time.Time, end string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.settle()
	if s.end != "" {
		return
	}
	if end == "eof" && err != nil && !errors.Is(err, io.EOF) {
		end = "error: " + err.Error()
	}
	s.end, s.endedAt = end, at
}

// stop cancels the connection and waits for the reader.
func (s *stream) stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// changeID returns the ID of the first change in the data of a patch event.
func changeID(data string) string {
	var d struct {
		Changes []struct {
			ID string `json:"id"`
		} `json:"changes"`
	}
	if json.Unmarshal([]byte(data), &d) != nil || len(d.Changes) == 0 {
		return ""
	}
	return d.Changes[0].ID
}

func reasonOf(data string) string {
	var d struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(data), &d)
	return d.Reason
}

// openStats counts the answers of the connect attempts of a group of streams.
type openStats struct {
	s503, s429, retries, maxAttempts, failed atomic.Int64
}

// open connects s with retries. since returns the cursor of each attempt ("" for the head).
// After a sync answer it loads the snapshot and connects with the snapshot cursor.
func (s *stream) open(ctx context.Context, since func() string, settled *sync.WaitGroup, st *openStats) error {
	var last string
	for attempt := range maxAttempts {
		if attempt > 0 {
			st.retries.Add(1)
		}
		for n := st.maxAttempts.Load(); n < int64(attempt+1) && !st.maxAttempts.CompareAndSwap(n, int64(attempt+1)); {
			n = st.maxAttempts.Load()
		}
		res := s.connect(ctx, since(), settled)
		var wait time.Duration
		switch {
		case res.err != nil:
			last = res.err.Error()
			wait = retryDelay(attempt, 0, s.run.random)
		case res.status == http.StatusOK:
			return nil
		case res.status == http.StatusServiceUnavailable || res.status == http.StatusTooManyRequests:
			if res.status == http.StatusServiceUnavailable {
				st.s503.Add(1)
			} else {
				st.s429.Add(1)
			}
			last = "status " + strconv.Itoa(res.status)
			wait = retryDelay(attempt, res.retryAfter, s.run.random)
		default:
			st.failed.Add(1)
			return fmt.Errorf("the stream answered %d", res.status)
		}
		select {
		case <-ctx.Done():
			st.failed.Add(1)
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	st.failed.Add(1)
	return fmt.Errorf("the stream did not open after %d attempts (last: %s)", maxAttempts, last)
}

// waitReady waits until every stream got ready, a stream ended, or the timeout.
func waitReady(wg *sync.WaitGroup, timeout time.Duration) bool {
	ch := make(chan struct{})
	go func() { wg.Wait(); close(ch) }()
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

// post sends the publish POST and returns the ID of the created row.
func (r *runner) post(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.PublishURL, strings.NewReader(r.cfg.PublishBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.cfg.Cookie != "" {
		req.Header.Set("Cookie", r.cfg.Cookie)
	}
	resp, err := r.httpClient(-1).Do(req)
	if err != nil {
		return "", err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("POST %s answered %d: %.160s", r.cfg.PublishURL, resp.StatusCode, body)
	}
	var item struct {
		ID json.Number `json:"id"`
	}
	if err := json.Unmarshal(body, &item); err != nil || item.ID == "" {
		return "", fmt.Errorf("POST %s returned no id: %.160s", r.cfg.PublishURL, body)
	}
	return item.ID.String(), nil
}

// snapshot loads the snapshot of the feed and returns its cursor.
func (r *runner) snapshot(ctx context.Context) (cursor, error) {
	u := r.cfg.Base + "/api/live/" + r.cfg.Feed
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return cursor{}, err
	}
	if r.cfg.Cookie != "" {
		req.Header.Set("Cookie", r.cfg.Cookie)
	}
	resp, err := r.httpClient(-1).Do(req)
	if err != nil {
		return cursor{}, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	_ = resp.Body.Close()
	if err != nil {
		return cursor{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return cursor{}, fmt.Errorf("GET %s answered %d: %.160s", u, resp.StatusCode, body)
	}
	var snap struct {
		Epoch *int64            `json:"epoch"`
		Seq   *int64            `json:"seq"`
		Rows  []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(body, &snap); err != nil || snap.Epoch == nil || snap.Seq == nil || snap.Rows == nil {
		return cursor{}, fmt.Errorf("GET %s returned no epoch, seq, and rows: %.160s", u, body)
	}
	return cursor{*snap.Epoch, *snap.Seq}, nil
}

// rssKiB reads the resident set size of a process with ps.
func rssKiB(pid int) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, fmt.Errorf("ps cannot read the RSS of process %d: %w", pid, err)
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

// exited reports whether the process is gone or a zombie.
func exited(pid int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	state := strings.TrimSpace(string(out))
	return err != nil || state == "" || strings.HasPrefix(state, "Z")
}

// ---------------------------------------------------------------------------
// Scenarios
// ---------------------------------------------------------------------------

// Run runs the scenarios of cfg and returns the result. It never returns an error: a failed
// check goes in Result.Failures.
func Run(ctx context.Context, cfg Config) Result {
	res := Result{Base: cfg.Base, Feed: cfg.Feed, Clients: cfg.Clients, Patches: cfg.Patches, Failures: []string{}}
	r := newRunner(cfg)
	defer r.close()
	fail := func(where string, err error) string {
		msg := where + ": " + err.Error()
		res.Failures = append(res.Failures, msg)
		return err.Error()
	}

	if err := r.conformance(ctx); err != nil {
		res.Conformance.Error = fail("conformance", err)
		return res
	}
	res.Conformance.OK = true

	var mem *Memory
	if cfg.ServerPID > 0 {
		mem = &Memory{}
		res.Memory = mem
		time.Sleep(500 * time.Millisecond)
		before, err := rssKiB(cfg.ServerPID)
		if err != nil {
			mem.Error = fail("memory", err)
			mem = nil
		} else {
			mem.BeforeKiB = before
		}
	}

	streams := make([]*stream, cfg.Clients)
	for i := range streams {
		streams[i] = &stream{run: r, client: r.httpClient(i)}
	}
	defer func() {
		var wg sync.WaitGroup
		for _, s := range streams {
			wg.Go(s.stop)
		}
		wg.Wait()
	}()

	fan := &Fanout{Clients: cfg.Clients, Patches: cfg.Patches}
	res.Fanout = fan
	var st openStats
	start := time.Now()
	if err := r.openAll(ctx, streams, func(*stream) string { return "" }, &st, false); err != nil {
		fan.Error = fail("fanout", err)
		return res
	}
	fan.OpenS = seconds(time.Since(start))
	fan.OpenRetry = st.retries.Load()

	if mem != nil {
		time.Sleep(time.Second)
		after, err := rssKiB(cfg.ServerPID)
		if err != nil {
			mem.Error = fail("memory", err)
		} else {
			mem.AfterKiB = after
			mem.PerStreamKiB = round(float64(after-mem.BeforeKiB)/float64(cfg.Clients), 2)
		}
	}

	if err := r.fanout(ctx, streams, fan); err != nil {
		fan.Error = fail("fanout", err)
		return res
	}

	if cfg.Resume {
		rs := &Resume{Clients: cfg.Clients}
		res.Resume = rs
		if err := r.resume(ctx, streams, rs); err != nil {
			rs.Error = fail("resume", err)
			return res
		}
	}

	if cfg.DrainPID > 0 {
		d := &Drain{Clients: cfg.Clients}
		res.Drain = d
		if err := r.drain(streams, d); err != nil {
			d.Error = fail("drain", err)
		}
	}
	return res
}

// conformance checks the routes before any timing: the snapshot answers, a stream sends
// ready, and one POST arrives as a patch with the ID of the new row.
func (r *runner) conformance(ctx context.Context) error {
	head, err := r.snapshot(ctx)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	wg.Add(1)
	s := &stream{run: r, client: r.httpClient(-1)}
	res := s.connect(ctx, head.String(), &wg)
	if res.err != nil {
		return fmt.Errorf("the stream request failed: %w", res.err)
	}
	if res.status != http.StatusOK {
		return fmt.Errorf("the stream answered %d, not 200", res.status)
	}
	defer s.stop()
	if !waitReady(&wg, r.cfg.Timeout) || !s.isReady() {
		return fmt.Errorf("the stream sent no ready event in %s (end: %q)", r.cfg.Timeout, s.endState())
	}
	id, err := r.post(ctx)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(r.cfg.Timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := slices.ContainsFunc(s.arrivals, func(a arrival) bool { return a.change == id })
		s.mu.Unlock()
		if got {
			r.patches.Store(0)
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("the patch of the new row %s did not arrive in %s (end: %q)", id, r.cfg.Timeout, s.endState())
}

func (s *stream) isReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

func (s *stream) endState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.end
}

// openAll opens every stream at one time (with retries) and waits until each stream got
// ready or ended. With allowSync, a stream that ended with sync is not an error.
func (r *runner) openAll(ctx context.Context, streams []*stream, since func(*stream) string, st *openStats, allowSync bool) error {
	var ready sync.WaitGroup
	ready.Add(len(streams))
	gate := make(chan struct{})
	errs := make(chan error, len(streams))
	var wg sync.WaitGroup
	for _, s := range streams {
		wg.Go(func() {
			<-gate
			if err := s.open(ctx, func() string { return since(s) }, &ready, st); err != nil {
				errs <- err
				ready.Done()
			}
		})
	}
	close(gate)
	wg.Wait()
	close(errs)
	if err, ok := <-errs; ok {
		return fmt.Errorf("%d streams did not open (first: %w)", 1+len(errs), err)
	}
	settled := waitReady(&ready, r.cfg.Timeout)
	n, first := 0, ""
	for _, s := range streams {
		s.mu.Lock()
		if !s.ready && (!allowSync || !strings.HasPrefix(s.end, "sync:")) {
			n++
			if first == "" {
				first = s.end
			}
		}
		s.mu.Unlock()
	}
	switch {
	case !settled:
		return fmt.Errorf("%d of %d streams sent no ready event in %s", n, len(streams), r.cfg.Timeout)
	case n > 0:
		return fmt.Errorf("%d of %d streams ended before the ready event (first end: %q)", n, len(streams), first)
	}
	return nil
}

// fanout publishes the patches and checks that every stream got each one, in order.
func (r *runner) fanout(ctx context.Context, streams []*stream, fan *Fanout) error {
	for _, s := range streams {
		s.mu.Lock()
		s.arrivals = s.arrivals[:0]
		s.mu.Unlock()
	}
	r.patches.Store(0)
	ends := make([]time.Time, r.cfg.Patches)
	ids := make([]string, r.cfg.Patches)
	posts := make([]time.Duration, 0, r.cfg.Patches)
	interval := time.Duration(float64(time.Second) / r.cfg.PublishRate)
	next := time.Now()
	for i := range ends {
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		}
		next = next.Add(interval)
		t := time.Now()
		id, err := r.post(ctx)
		if err != nil {
			return err
		}
		ends[i] = time.Now()
		posts = append(posts, ends[i].Sub(t))
		ids[i] = id
	}
	fan.PublishMS = percentiles(posts)
	want := int64(len(streams) * r.cfg.Patches)
	deadline := time.Now().Add(r.cfg.Timeout)
	for r.patches.Load() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	lat := make([]time.Duration, 0, want)
	for _, s := range streams {
		s.mu.Lock()
		st := checkArrivals(s.head.seq, s.arrivals, ends, ids)
		s.mu.Unlock()
		lat = append(lat, st.latencies...)
		fan.Early += st.early
		fan.Missed += st.missed
		fan.Duplicates += st.duplicates
		fan.OutOfOrder += st.outOfOrder
		fan.Mismatched += st.mismatched
	}
	fan.Samples = len(lat)
	fan.Latency = percentiles(lat)
	fan.OrderOK = fan.OutOfOrder == 0 && fan.Mismatched == 0
	if fan.Missed > 0 || fan.Duplicates > 0 || !fan.OrderOK {
		return fmt.Errorf("%d missed, %d duplicate, %d out-of-order, and %d mismatched patches", fan.Missed,
			fan.Duplicates, fan.OutOfOrder, fan.Mismatched)
	}
	return nil
}

// arrivalStats is the check of the patch frames of one stream.
type arrivalStats struct {
	latencies                                         []time.Duration
	early, missed, duplicates, outOfOrder, mismatched int
}

// checkArrivals matches the patch frames of one stream to the POST requests. The patch of
// POST i has the sequence head+1+i. ends holds the end time of each POST, and ids the row
// ID that each POST created. A frame that arrives before its POST ended counts as early,
// with a latency of 0.
func checkArrivals(head int64, arrivals []arrival, ends []time.Time, ids []string) arrivalStats {
	var st arrivalStats
	seen := make([]bool, len(ends))
	prev := head
	for _, a := range arrivals {
		if a.seq <= prev {
			if a.seq > head && a.seq <= head+int64(len(ends)) && seen[a.seq-head-1] {
				st.duplicates++
			} else {
				st.outOfOrder++
			}
			continue
		}
		if a.seq != prev+1 {
			st.outOfOrder++
		}
		prev = a.seq
		i := a.seq - head - 1
		if i < 0 || i >= int64(len(ends)) {
			st.mismatched++
			continue
		}
		seen[i] = true
		if a.change != ids[i] {
			st.mismatched++
		}
		d := a.at.Sub(ends[i])
		if d < 0 {
			st.early++
			d = 0
		}
		st.latencies = append(st.latencies, d)
	}
	for _, ok := range seen {
		if !ok {
			st.missed++
		}
	}
	return st
}

// resume closes every stream at one time, then opens them again at one time with since.
func (r *runner) resume(ctx context.Context, streams []*stream, rs *Resume) error {
	var wg sync.WaitGroup
	for _, s := range streams {
		wg.Go(s.stop)
	}
	wg.Wait()
	r.syncs.Store(0)
	var st openStats
	start := time.Now()
	since := func(s *stream) string {
		s.mu.Lock()
		defer s.mu.Unlock()
		if strings.HasPrefix(s.end, "sync:") {
			return ""
		}
		return s.last.String()
	}
	err := r.openAll(ctx, streams, since, &st, true)
	// A stream that got sync loads the snapshot and connects again with its cursor.
	for round := 0; err == nil; round++ {
		var again []*stream
		for _, s := range streams {
			s.mu.Lock()
			if !s.ready && strings.HasPrefix(s.end, "sync:") {
				again = append(again, s)
			}
			s.mu.Unlock()
		}
		if len(again) == 0 {
			break
		}
		if round == 3 {
			err = fmt.Errorf("%d streams still got sync after 3 snapshot loads", len(again))
			break
		}
		head, serr := r.snapshot(ctx)
		if serr != nil {
			err = serr
			break
		}
		for _, s := range again {
			s.mu.Lock()
			s.last, s.end = head, ""
			s.mu.Unlock()
		}
		err = r.openAll(ctx, again, func(*stream) string { return head.String() }, &st, true)
	}
	rs.ReadyS = seconds(time.Since(start))
	rs.Sync = r.syncs.Load()
	rs.Status503, rs.Status429 = st.s503.Load(), st.s429.Load()
	rs.Retries, rs.MaxAttempts, rs.Failed = st.retries.Load(), st.maxAttempts.Load(), st.failed.Load()
	for _, s := range streams {
		s.mu.Lock()
		if s.resumed {
			rs.Resumed++
		}
		s.mu.Unlock()
	}
	return err
}

// drain sends SIGTERM to the server and measures the end of the streams and of the process.
func (r *runner) drain(streams []*stream, d *Drain) error {
	start := time.Now()
	// os.Process.Signal compiles on every platform. syscall.Kill does not exist on Windows.
	proc, err := os.FindProcess(r.cfg.DrainPID)
	if err != nil {
		return fmt.Errorf("cannot find process %d: %w", r.cfg.DrainPID, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("cannot send SIGTERM to process %d: %w", r.cfg.DrainPID, err)
	}
	deadline := start.Add(r.cfg.Timeout)
	for _, s := range streams {
		s.mu.Lock()
		done := s.done
		s.mu.Unlock()
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-time.After(time.Until(deadline)):
		}
	}
	var last time.Time
	for _, s := range streams {
		s.mu.Lock()
		switch s.end {
		case "":
			d.Open++
		case "bye:drain":
			d.Bye++
		default:
			d.NoBye++
		}
		if s.endedAt.After(last) {
			last = s.endedAt
		}
		s.mu.Unlock()
	}
	if !last.IsZero() {
		d.EndedS = seconds(last.Sub(start))
	}
	for time.Now().Before(deadline) {
		if exited(r.cfg.DrainPID) {
			d.Exited = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	d.ExitS = seconds(time.Since(start))
	switch {
	case d.Open > 0:
		return fmt.Errorf("%d streams were still open %s after SIGTERM", d.Open, r.cfg.Timeout)
	case !d.Exited:
		return fmt.Errorf("process %d did not exit in %s after SIGTERM", r.cfg.DrainPID, r.cfg.Timeout)
	case d.NoBye > 0:
		return fmt.Errorf("%d streams ended without a bye event with the reason drain", d.NoBye)
	}
	return nil
}
