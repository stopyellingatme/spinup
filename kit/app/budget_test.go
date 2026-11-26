package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	middlewareGeneratedAllocsCeiling = 26
	middlewareProvidedAllocsCeiling  = 25
	healthAllocsCeiling              = 29
	spaAssetAllocsCeiling            = 48
	spaIndexAllocsCeiling            = 48
	spaIndexBrotliAllocsCeiling      = 50
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

func TestHealthBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("allocation budgets do not run with race or short tests")
	}
	allocs := testing.AllocsPerRun(200, benchmarkHealthBody())
	assertAllocationBudget(t, allocs, healthAllocsCeiling)
}

func TestSPAAssetBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("allocation budgets do not run with race or short tests")
	}
	allocs := testing.AllocsPerRun(200, newBenchmarkSPABody("/_app/immutable/app.js", false))
	assertAllocationBudget(t, allocs, spaAssetAllocsCeiling)
	t.Run("brotli", func(t *testing.T) {
		allocs := testing.AllocsPerRun(200, newBenchmarkSPABody("/_app/immutable/app.js", true))
		assertAllocationBudget(t, allocs, spaAssetAllocsCeiling)
	})
}

func TestSPAIndexBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("allocation budgets do not run with race or short tests")
	}
	allocs := testing.AllocsPerRun(200, newBenchmarkSPABody("/deep/link", false))
	assertAllocationBudget(t, allocs, spaIndexAllocsCeiling)
	t.Run("brotli", func(t *testing.T) {
		allocs := testing.AllocsPerRun(200, newBenchmarkSPABody("/deep/link", true))
		assertAllocationBudget(t, allocs, spaIndexBrotliAllocsCeiling)
	})
}

func assertAllocationBudget(t *testing.T, measured float64, ceiling int) {
	t.Helper()
	if measured > float64(ceiling) {
		t.Fatalf("measured %.0f allocations, ceiling %d; raise the ceiling in this file only with a reason in the commit message", measured, ceiling)
	}
}
