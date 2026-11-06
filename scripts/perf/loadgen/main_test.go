package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunCountsRequests(t *testing.T) {
	var hits atomic.Int64
	var cookies atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Cookie") == "session=abc" {
			cookies.Add(1)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	res := Run(context.Background(), Config{URL: srv.URL, Concurrency: 4, Requests: 200, Cookie: "session=abc",
		Expect: http.StatusOK, Timeout: 5 * time.Second})
	if res.Requests != 200 || res.Errors != 0 || res.Status["200"] != 200 {
		t.Fatalf("result = %+v, want 200 requests with status 200 and no errors", res)
	}
	if hits.Load() != 200 || cookies.Load() != 200 {
		t.Fatalf("server saw %d requests (%d with the cookie), want 200", hits.Load(), cookies.Load())
	}
	l := res.Latency
	if l.P50 <= 0 || l.P50 > l.P90 || l.P90 > l.P99 || l.P99 > l.P999 || l.P999 > l.Max {
		t.Fatalf("latency percentiles are not ordered: %+v", l)
	}
	if res.RPS <= 0 {
		t.Fatalf("rps = %v, want > 0", res.RPS)
	}
}

func TestRunDurationAndWarmup(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := Run(context.Background(), Config{URL: srv.URL, Concurrency: 2, Duration: 100 * time.Millisecond,
		Warmup: 50 * time.Millisecond, Expect: http.StatusOK, Timeout: 5 * time.Second})
	if res.Requests == 0 || res.Errors != 0 {
		t.Fatalf("result = %+v, want requests and no errors", res)
	}
	if hits.Load() <= res.Requests {
		t.Fatalf("server saw %d requests, want more than the %d measured ones (the warm-up)", hits.Load(), res.Requests)
	}
}

func TestRunCountsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	res := Run(context.Background(), Config{URL: srv.URL, Concurrency: 2, Requests: 10, Expect: http.StatusOK,
		Timeout: 5 * time.Second})
	if res.Errors != 10 || res.Status["429"] != 10 || !strings.Contains(res.FirstError, "429") {
		t.Fatalf("result = %+v, want 10 errors with status 429", res)
	}
}

func TestPercentiles(t *testing.T) {
	var xs []time.Duration
	for i := 100; i >= 1; i-- {
		xs = append(xs, time.Duration(i)*time.Millisecond)
	}
	l := percentiles(xs)
	if l.P50 != 50 || l.P90 != 90 || l.P99 != 99 || l.P999 != 100 || l.Max != 100 || l.Mean != 50.5 {
		t.Fatalf("percentiles = %+v", l)
	}
	if got := percentiles(nil); got != (Latency{}) {
		t.Fatalf("percentiles(nil) = %+v, want zero", got)
	}
}

func TestParseFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-d", "1s"}, "-url is missing"},
		{[]string{"-url", "http://x/"}, "exactly one of -n and -d"},
		{[]string{"-url", "http://x/", "-n", "1", "-d", "1s"}, "exactly one of -n and -d"},
		{[]string{"-url", "ftp://x/", "-n", "1"}, "not an http or https URL"},
		{[]string{"-url", "http://x/", "-n", "1", "-c", "0"}, "-c is 0"},
	} {
		_, err := parseFlags(tc.args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("parseFlags(%q) = %v, want an error with %q and a Try: line", tc.args, err, tc.want)
		}
	}
	cfg, err := parseFlags([]string{"-url", "http://127.0.0.1:1/healthz", "-n", "5", "-cookie", "a=b"}, io.Discard)
	if err != nil || cfg.Requests != 5 || cfg.Concurrency != 32 || cfg.Cookie != "a=b" || cfg.Expect != 200 {
		t.Fatalf("parseFlags = %+v, %v", cfg, err)
	}
}
