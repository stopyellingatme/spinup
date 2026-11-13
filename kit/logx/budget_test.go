package logx_test

import "testing"

const (
	accessLinePlainAllocsCeiling = 5
	accessLineURLAllocsCeiling   = 11
)

func TestAccessLineBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("allocation budgets do not run with race or short tests")
	}
	plainAllocs := testing.AllocsPerRun(200, newBenchmarkAccessLineBody("/api/notes"))
	assertAllocationBudget(t, plainAllocs, accessLinePlainAllocsCeiling)
	urlAllocs := testing.AllocsPerRun(200, newBenchmarkAccessLineBody("/proxy/https://user:password@example.com/notes"))
	assertAllocationBudget(t, urlAllocs, accessLineURLAllocsCeiling)
}

func assertAllocationBudget(t *testing.T, measured float64, ceiling int) {
	t.Helper()
	if measured > float64(ceiling) {
		t.Fatalf("measured %.0f allocations, ceiling %d; raise the ceiling in this file only with a reason in the commit message", measured, ceiling)
	}
}
