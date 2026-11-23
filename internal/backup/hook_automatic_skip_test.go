package backup

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// TestHookAutomaticSkipChecksPendingMigrations applies the migration guard when the deploy automatically skips a backup.
func TestHookAutomaticSkipChecksPendingMigrations(t *testing.T) {
	t.Parallel()
	for _, safe := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsafe", true: "safe"}[safe], func(t *testing.T) {
			t.Parallel()
			sql := "-- +goose Up\nDELETE FROM invoices;\n"
			if safe {
				sql = "-- +goose Up\n-- spinup:safe\nSELECT 1;\n"
			}
			dir := newProject(t, true, "", map[string]string{"00002_invoices.sql": sql})
			vm := &remote.Fake{}
			releaseHistory(vm, `[{"migration_version":"1"}]`)
			vm.Expect(psRunning...).Return("postgres\n", "", 0)
			vm.Expect(append(append([]string(nil), psql...), gooseTableQuery)...).Return("0\n", "", 0)
			vm.Expect(append(append([]string(nil), psql...), gooseAppliedQuery)...).Return("1\n", "", 0)
			ctx := deploy.WithHookOptions(t.Context(), deploy.HookOptions{Root: dir, SkipBackup: true})
			ctx = WithDeployOptions(ctx, DeployOptions{Dir: dir})
			results := map[string]string{}
			err := Hook(ctx, &deploy.HookContext{App: "todo", Remote: vm, Results: results})
			if safe {
				if err != nil || results[deploy.ResultMigrationsSafe] != "true" {
					t.Fatalf("safe skip: results = %v, error = %v", results, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "00002_invoices.sql") || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("unsafe skip: error = %v", err)
			}
			vm.AssertDone(t)
		})
	}
}
