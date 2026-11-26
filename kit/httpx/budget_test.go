package httpx

import "testing"

const (
	decodeJSONAllocsCeiling    = 4
	writeJSONAllocsCeiling     = 16
	checkJSONBodyAllocsCeiling = 8
)

func TestDecodeJSONBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("allocation budgets do not run with race or short tests")
	}
	body := benchmarkDecodeJSONBody(benchmarkSmallJSONBody(), "DecodeJSON")
	allocs := testing.AllocsPerRun(200, body)
	assertAllocationBudget(t, allocs, decodeJSONAllocsCeiling)
}

func TestWriteJSONBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("allocation budgets do not run with race or short tests")
	}
	body := benchmarkWriteJSONBody()
	allocs := testing.AllocsPerRun(200, body)
	assertAllocationBudget(t, allocs, writeJSONAllocsCeiling)
}

func TestCheckJSONBodyBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("allocation budgets do not run with race or short tests")
	}
	rules := BodyRules{Required: []string{"title", "body", "published"}, Known: []string{"title", "body", "published"}}
	body := benchmarkCheckJSONBody(benchmarkSmallJSONBody(), rules)
	allocs := testing.AllocsPerRun(200, body)
	assertAllocationBudget(t, allocs, checkJSONBodyAllocsCeiling)
}

func assertAllocationBudget(t *testing.T, measured float64, ceiling int) {
	t.Helper()
	if measured > float64(ceiling) {
		t.Fatalf("measured %.0f allocations, ceiling %d; raise the ceiling in this file only with a reason in the commit message", measured, ceiling)
	}
}
