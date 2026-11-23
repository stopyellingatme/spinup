package backup

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestHookSkipsUnrecordedRelease(t *testing.T) {
	t.Parallel()
	dir := newProject(t, true, "", nil)
	vm := &remote.Fake{}
	releaseHistory(vm, "")
	var out strings.Builder
	ctx := WithDeployOptions(t.Context(), DeployOptions{Dir: dir})
	if err := Hook(ctx, &deploy.HookContext{App: "todo", Remote: vm, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no release is recorded") {
		t.Fatalf("output = %s", out.String())
	}
	vm.AssertDone(t)
}
