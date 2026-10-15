package backup

import (
	"context"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestPruneAndPlan(t *testing.T) {
	t.Parallel()
	for _, plan := range []bool{false, true} {
		vm := &remote.Fake{}
		vm.ExpectUpload(RemoteConfigPath("todo"))
		args := []string{"expire"}
		if plan {
			args = append(args, "--dry-run")
		}
		vm.Expect(Command("todo", args...)...).Return("retention preview\n", "", 0)
		vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
		var out, progress strings.Builder
		bs, err := Prune(context.Background(), PruneOptions{Options: Options{Dir: newProject(t, true, "", nil), Env: "staging", Remote: vm, Out: &out, ErrOut: &progress}, Plan: plan})
		if err != nil || len(bs) != 2 || !strings.Contains(out.String(), restoreLabel) {
			t.Fatalf("prune: %s %v", out.String(), err)
		}
		vm.AssertDone(t)
		if len(vm.Uploads()) != 1 || vm.Uploads()[0].Mode != 0o600 {
			t.Fatal("prune did not upload configuration once with mode 0600")
		}
	}
}
