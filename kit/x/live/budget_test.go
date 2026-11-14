package live

import (
	"net/http"
	"net/url"
	"testing"
)

func TestFanoutBudget(t *testing.T) {
	body := fanoutBody()
	if allocs := testing.AllocsPerRun(10, func() {
		for range 100 {
			body()
		}
	}); allocs != 0 {
		t.Fatalf("fanout allocations: %g; budget: 0", allocs)
	}
}

func TestIsStreamBudget(t *testing.T) {
	r := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/live/notes/live"}}
	if allocs := testing.AllocsPerRun(100, func() {
		if !IsStream(r) {
			panic("not a stream")
		}
	}); allocs != 0 {
		t.Fatalf("IsStream allocations: %g; budget: 0", allocs)
	}
}
