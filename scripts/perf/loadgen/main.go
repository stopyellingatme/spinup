// Command loadgen is the HTTP load generator of the spinup performance loop (scripts/perf).
//
// It replaces Apache Bench (ab). N workers send keep-alive requests to one URL, for a fixed
// request count or for a fixed duration, after an optional warm-up. The workers run on all
// cores (GOMAXPROCS is the core count), so one client process does not limit a fast server.
// It writes one JSON object to standard output:
//
//	{"url": "...", "concurrency": 32, "requests": 100000, "errors": 0, "duration_s": 1.52,
//	 "rps": 65789.4, "latency_ms": {"p50": 0.41, "p90": 0.8, "p99": 1.9, "p999": 4.2, "max": 9.1, "mean": 0.48},
//	 "status": {"200": 100000}, "first_error": ""}
//
// A request is an error when the transport fails or when the status is not -expect. The
// warm-up requests are not in the result. The exit code is 0 when no request failed, 1 when a
// request failed (the JSON is still written), and 2 for a usage error.
//
// Examples:
//
//	go run ./scripts/perf/loadgen -url http://127.0.0.1:8080/healthz -c 32 -d 5s -warmup 1s
//	go run ./scripts/perf/loadgen -url http://127.0.0.1:8080/api/notes?limit=50 -n 20000 -cookie 'session=abc'
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config is the parsed command line of one load run.
type Config struct {
	URL         string
	Concurrency int
	Requests    int64
	Duration    time.Duration
	Warmup      time.Duration
	Cookie      string
	Expect      int
	Timeout     time.Duration
	Headers     http.Header
	ExpectBody  string
}

// Latency holds the latency percentiles of a run in milliseconds (nearest rank).
type Latency struct {
	P50  float64 `json:"p50"`
	P90  float64 `json:"p90"`
	P99  float64 `json:"p99"`
	P999 float64 `json:"p999"`
	Max  float64 `json:"max"`
	Mean float64 `json:"mean"`
}

// Result is the JSON output of one load run.
type Result struct {
	URL         string         `json:"url"`
	Concurrency int            `json:"concurrency"`
	Requests    int64          `json:"requests"`
	Errors      int64          `json:"errors"`
	DurationS   float64        `json:"duration_s"`
	RPS         float64        `json:"rps"`
	Latency     Latency        `json:"latency_ms"`
	TTFB        Latency        `json:"ttfb_ms"`
	Bytes       int64          `json:"bytes_received"`
	Status      map[string]int `json:"status"`
	FirstError  string         `json:"first_error"`
	WarmupS     float64        `json:"warmup_s"`
	GOMAXPROCS  int            `json:"gomaxprocs"`
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
	ctx := context.Background()
	res := Run(ctx, cfg)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		fmt.Fprintf(os.Stderr, "loadgen: cannot write the result: %v\nTry: check standard output\n", err)
		os.Exit(1)
	}
	if res.Errors > 0 {
		fmt.Fprintf(os.Stderr, "loadgen: %d of %d requests failed (first: %s)\nTry: check the server log and the URL\n",
			res.Errors, res.Requests, res.FirstError)
		os.Exit(1)
	}
}

// parseFlags reads the command line. It returns a usage error with a Try: line.
func parseFlags(args []string, out io.Writer) (Config, error) {
	fs := flag.NewFlagSet("loadgen", flag.ContinueOnError)
	fs.SetOutput(out)
	var cfg Config
	fs.StringVar(&cfg.URL, "url", "", "the URL to load (required)")
	fs.IntVar(&cfg.Concurrency, "c", 32, "the number of concurrent keep-alive workers")
	fs.Int64Var(&cfg.Requests, "n", 0, "the number of measured requests (or use -d)")
	fs.DurationVar(&cfg.Duration, "d", 0, "the measured duration (or use -n)")
	fs.DurationVar(&cfg.Warmup, "warmup", 0, "the warm-up duration before the measured requests")
	fs.StringVar(&cfg.Cookie, "cookie", "", "the value of a Cookie header for every request")
	fs.IntVar(&cfg.Expect, "expect", http.StatusOK, "the expected status code (another status is an error)")
	fs.DurationVar(&cfg.Timeout, "timeout", 10*time.Second, "the timeout of one request")
	fs.Var(headerValue{headers: &cfg.Headers}, "header", "an HTTP header for every request, in 'Name: value' form")
	fs.StringVar(&cfg.ExpectBody, "expect-body", "", "a body substring required in every response")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	switch {
	case cfg.URL == "":
		return cfg, errors.New("loadgen: -url is missing\nTry: loadgen -url http://127.0.0.1:8080/healthz -d 5s")
	case cfg.Concurrency < 1:
		return cfg, fmt.Errorf("loadgen: -c is %d, and it must be 1 or more\nTry: -c 32", cfg.Concurrency)
	case (cfg.Requests > 0) == (cfg.Duration > 0):
		return cfg, errors.New("loadgen: set exactly one of -n and -d\nTry: -n 20000 or -d 5s")
	case cfg.Requests < 0 || cfg.Duration < 0 || cfg.Warmup < 0 || cfg.Timeout <= 0:
		return cfg, errors.New("loadgen: -n, -d, -warmup, and -timeout cannot be negative\nTry: -d 5s -warmup 1s")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return cfg, fmt.Errorf("loadgen: -url %q is not an http or https URL\nTry: -url http://127.0.0.1:8080/healthz", cfg.URL)
	}
	return cfg, nil
}

// headerValue parses one repeatable -header flag.
type headerValue struct{ headers *http.Header }

func (v headerValue) String() string { return "" }

func (v headerValue) Set(value string) error {
	name, content, ok := strings.Cut(value, ":")
	name = strings.TrimSpace(name)
	content = strings.TrimSpace(content)
	if !ok || name == "" || content == "" {
		return fmt.Errorf("loadgen: -header %q is not 'Name: value'\nTry: -header 'Authorization: Bearer token'", value)
	}
	if *v.headers == nil {
		*v.headers = make(http.Header)
	}
	v.headers.Add(name, content)
	return nil
}

// newTransport returns a keep-alive transport with one idle connection for each worker.
func newTransport(cfg Config) *http.Transport {
	return &http.Transport{
		DialContext:         (&net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        cfg.Concurrency,
		MaxIdleConnsPerHost: cfg.Concurrency,
		MaxConnsPerHost:     cfg.Concurrency,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
}

// worker is the state of one worker goroutine.
type worker struct {
	lat    []time.Duration
	ttfb   []time.Duration
	status map[int]int
	bytes  int64
	errs   int64
	first  string
}

// Run sends the load of cfg and returns the result. It never returns an error: failed
// requests are counted in Result.Errors.
func Run(ctx context.Context, cfg Config) Result {
	tr := newTransport(cfg)
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	if cfg.Warmup > 0 {
		wctx, cancel := context.WithTimeout(ctx, cfg.Warmup)
		phase(wctx, client, cfg, nil, false)
		cancel()
	}

	var budget *atomic.Int64
	mctx := ctx
	if cfg.Requests > 0 {
		budget = new(atomic.Int64)
		budget.Store(cfg.Requests)
	} else {
		var cancel context.CancelFunc
		mctx, cancel = context.WithTimeout(ctx, cfg.Duration)
		defer cancel()
	}
	start := time.Now()
	ws := phase(mctx, client, cfg, budget, true)
	elapsed := time.Since(start)
	return summarize(cfg, ws, elapsed)
}

// phase runs cfg.Concurrency workers until ctx ends or the budget is spent. With record set,
// each worker keeps its latencies and status counts.
func phase(ctx context.Context, client *http.Client, cfg Config, budget *atomic.Int64, record bool) []*worker {
	ws := make([]*worker, cfg.Concurrency)
	var wg sync.WaitGroup
	for i := range ws {
		w := &worker{status: map[int]int{}}
		if record && budget != nil {
			w.lat = make([]time.Duration, 0, budget.Load()/int64(cfg.Concurrency)+1)
		}
		ws[i] = w
		wg.Go(func() { w.loop(ctx, client, cfg, budget, record) })
	}
	wg.Wait()
	return ws
}

func (w *worker) loop(ctx context.Context, client *http.Client, cfg Config, budget *atomic.Int64, record bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		w.errs++
		w.first = err.Error()
		return
	}
	if cfg.Cookie != "" {
		req.Header.Set("Cookie", cfg.Cookie)
	}
	for name, values := range cfg.Headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	for ctx.Err() == nil {
		if budget != nil && budget.Add(-1) < 0 {
			return
		}
		t := time.Now()
		code, received, firstByte, responseBody, err := do(client, req)
		d := time.Since(t)
		if err != nil && ctx.Err() != nil {
			return // the duration ended during the request: the request does not count
		}
		if !record {
			continue
		}
		w.lat = append(w.lat, d)
		if firstByte > 0 {
			w.ttfb = append(w.ttfb, firstByte)
		}
		w.bytes += received
		switch {
		case err != nil:
			w.errs++
			if w.first == "" {
				w.first = err.Error()
			}
		case code != cfg.Expect:
			w.status[code]++
			w.errs++
			if w.first == "" {
				w.first = fmt.Sprintf("status %d, expected %d", code, cfg.Expect)
			}
		case cfg.ExpectBody != "" && !bytes.Contains(responseBody, []byte(cfg.ExpectBody)):
			w.status[code]++
			w.errs++
			if w.first == "" {
				w.first = fmt.Sprintf("response body does not contain %q", cfg.ExpectBody)
			}
		default:
			w.status[code]++
		}
	}
}

// do sends one request and reads the whole body, so the connection goes back to the pool.
func do(client *http.Client, req *http.Request) (int, int64, time.Duration, []byte, error) {
	start := time.Now()
	var firstByte time.Time
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { firstByte = time.Now() }}
	resp, err := client.Do(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err != nil {
		return 0, 0, 0, nil, err
	}
	body, err := io.ReadAll(resp.Body)
	cerr := resp.Body.Close()
	if err == nil {
		err = cerr
	}
	ttfb := time.Duration(0)
	if !firstByte.IsZero() {
		ttfb = firstByte.Sub(start)
	}
	return resp.StatusCode, int64(len(body)), ttfb, body, err
}

// summarize merges the worker states into one Result.
func summarize(cfg Config, ws []*worker, elapsed time.Duration) Result {
	res := Result{URL: cfg.URL, Concurrency: cfg.Concurrency, Status: map[string]int{},
		DurationS: round(elapsed.Seconds(), 3), WarmupS: cfg.Warmup.Seconds(), GOMAXPROCS: runtime.GOMAXPROCS(0)}
	var all []time.Duration
	var allTTFB []time.Duration
	for _, w := range ws {
		all = append(all, w.lat...)
		allTTFB = append(allTTFB, w.ttfb...)
		res.Bytes += w.bytes
		res.Errors += w.errs
		if res.FirstError == "" {
			res.FirstError = w.first
		}
		for code, n := range w.status {
			res.Status[strconv.Itoa(code)] += n
		}
	}
	res.Requests = int64(len(all))
	if res.Requests == 0 && res.Errors > 0 {
		res.Requests = res.Errors
	}
	if elapsed > 0 {
		res.RPS = round(float64(len(all))/elapsed.Seconds(), 1)
	}
	res.Latency = percentiles(all)
	res.TTFB = percentiles(allTTFB)
	return res
}

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
		P999: ms(rank(xs, 0.999)),
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

func round(x float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(x*p) / p
}
