// Command load sends GET requests in a loop from several workers and counts
// the results. SIGUSR1 marks the start of the switch window, SIGUSR2 marks
// its end, and SIGTERM (or SIGINT) stops the run. On stop it waits for the
// requests in flight, then prints one RESULT line with key=value pairs for run.sh.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type counters struct {
	total, ok, non200, connErr, slow, maxMS atomic.Int64
}

func (c *counters) add(code int, err error, took time.Duration) {
	c.total.Add(1)
	ms := took.Milliseconds()
	if ms >= 1000 {
		c.slow.Add(1)
	}
	for {
		m := c.maxMS.Load()
		if ms <= m || c.maxMS.CompareAndSwap(m, ms) {
			break
		}
	}
	switch {
	case err != nil:
		c.connErr.Add(1)
	case code != http.StatusOK:
		c.non200.Add(1)
	default:
		c.ok.Add(1)
	}
}

func main() {
	url := flag.String("url", "http://bg-caddy:8080/", "target URL")
	workers := flag.Int("c", 8, "parallel workers")
	reuse := flag.Bool("reuse", false, "keep one idle connection per worker (default: Go keeps 2, so most requests open a new connection)")
	flag.Parse()

	var all, win counters
	// phase is read when a request is sent: 0 before the window, 1 in it, 2 after it.
	var phase atomic.Int32
	var mu sync.Mutex
	colors := [3]map[string]int64{{}, {}, {}} // phase -> body color -> count

	// On stop, workers send no new request, and every request in flight
	// finishes (or hits the client timeout) and is counted.
	var stopping atomic.Bool
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if *reuse {
		tr.MaxIdleConnsPerHost = *workers
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: tr}
	var wg sync.WaitGroup
	for range *workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stopping.Load() {
				ph := phase.Load()
				t0 := time.Now()
				code, body, err := get(client, *url)
				took := time.Since(t0)
				all.add(code, err, took)
				if ph == 1 {
					win.add(code, err, took)
				}
				if err == nil && code == http.StatusOK {
					color, _, _ := strings.Cut(body, " ")
					mu.Lock()
					colors[ph][color]++
					mu.Unlock()
				} else {
					fmt.Fprintf(os.Stderr, "FAIL %s code=%d took=%s err=%v\n", time.Now().Format(time.RFC3339Nano), code, took, err)
				}
			}
		}()
	}

	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGTERM, os.Interrupt)
	var start, end time.Time
	for s := range sig {
		if s == syscall.SIGUSR1 {
			start = time.Now()
			phase.Store(1)
			continue
		}
		if s == syscall.SIGUSR2 {
			end = time.Now()
			phase.Store(2)
			continue
		}
		break
	}
	stopping.Store(true)
	wg.Wait()
	if end.IsZero() {
		end = time.Now()
	}
	if start.IsZero() {
		start = end
	}
	mu.Lock()
	defer mu.Unlock()
	// pre_* and post_* count the colors that answered requests sent before and after the window.
	// slow counts requests that took 1 s or more. max_ms is the slowest request.
	fmt.Printf("RESULT total=%d ok=%d non200=%d connerr=%d slow=%d max_ms=%d "+
		"win_total=%d win_ok=%d win_non200=%d win_connerr=%d win_slow=%d win_max_ms=%d win_ms=%d "+
		"pre_blue=%d pre_green=%d win_blue=%d win_green=%d post_blue=%d post_green=%d\n",
		all.total.Load(), all.ok.Load(), all.non200.Load(), all.connErr.Load(), all.slow.Load(), all.maxMS.Load(),
		win.total.Load(), win.ok.Load(), win.non200.Load(), win.connErr.Load(), win.slow.Load(), win.maxMS.Load(),
		end.Sub(start).Milliseconds(),
		colors[0]["blue"], colors[0]["green"], colors[1]["blue"], colors[1]["green"], colors[2]["blue"], colors[2]["green"])
}

func get(client *http.Client, url string) (int, string, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", err
	}
	return resp.StatusCode, string(b), nil
}
