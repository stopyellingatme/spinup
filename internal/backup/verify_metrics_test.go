package backup

import (
	"context"
	"testing"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestVerifyRecordsMetrics(t *testing.T) {
	previous := recordMetrics
	t.Cleanup(func() { recordMetrics = previous })
	for _, fail := range []bool{false, true} {
		vm := &remote.Fake{}
		vm.ExpectUpload(RemoteConfigPath("todo"))
		expectRestoreContainer(t, vm, true, fail)
		var recorded Sample
		calls := 0
		recordMetrics = func(_ context.Context, r remote.Runner, app string, s Sample) error {
			if r != vm || app != "todo" {
				t.Fatal("metrics used a different remote or app")
			}
			recorded = s
			calls++
			return nil
		}
		_, err := Verify(context.Background(), Options{Dir: newProject(t, true, "", nil), Env: "staging", Remote: vm}, Target{Label: restoreLabel})
		if (err != nil) != fail || calls != 1 || recorded.Verified.IsZero() || recorded.VerifyOK == fail || recorded.RTO <= 0 {
			t.Fatalf("metrics: %+v calls=%d err=%v", recorded, calls, err)
		}
		vm.AssertDone(t)
	}
}
