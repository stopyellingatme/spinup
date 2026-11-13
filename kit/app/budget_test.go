package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	middlewareGeneratedAllocsCeiling = 30
	middlewareProvidedAllocsCeiling  = 29
	spaAssetAllocsCeiling            = 53
	spaIndexAllocsCeiling            = 53
)

func TestMiddlewareChainBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("allocation budgets do not run with race or short tests")
	}
	handler := New(WithLogger(discardLogger()), WithRoutes(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /bench", func(http.ResponseWriter, *http.Request) {})
	})).Handler()

	generated := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bench", nil)
	generatedAllocs := testing.AllocsPerRun(200, benchmarkMiddlewareChainBody(handler, generated))
	assertAllocationBudget(t, generatedAllocs, middlewareGeneratedAllocsCeiling)

	provided := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bench", nil)
	provided.Header.Set("X-Request-ID", "benchmark-request-id")
	providedAllocs := testing.AllocsPerRun(200, benchmarkMiddlewareChainBody(handler, provided))
	assertAllocationBudget(t, providedAllocs, middlewareProvidedAllocsCeiling)
}

func TestSPAAssetBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("allocation budgets do not run with race or short tests")
	}
	allocs := testing.AllocsPerRun(200, newBenchmarkSPABody("/_app/immutable/app.js"))
	assertAllocationBudget(t, allocs, spaAssetAllocsCeiling)
}

func TestSPAIndexBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("allocation budgets do not run with race or short tests")
	}
	allocs := testing.AllocsPerRun(200, newBenchmarkSPABody("/deep/link"))
	assertAllocationBudget(t, allocs, spaIndexAllocsCeiling)
}

func assertAllocationBudget(t *testing.T, measured float64, ceiling int) {
	t.Helper()
	if measured > float64(ceiling) {
		t.Fatalf("measured %.0f allocations, ceiling %d; raise the ceiling in this file only with a reason in the commit message", measured, ceiling)
	}
}
