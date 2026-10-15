package apptest

import (
	"bytes"
	"context"
	"strings"
	"testing"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestRun(t *testing.T) {
	t.Parallel()
	for _, code := range []int{0, 7} {
		t.Run(string(rune('0'+code)), func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			fake := &exec.Fake{}
			fake.Expect("go", "test", "-run", "Todo", "-race", "-cover", "./...").Return("tests\n", "", code)
			err := Run(context.Background(), Options{Root: t.TempDir(), Run: "Todo", Race: true, Cover: true, Out: &out}, fake)
			if ui.ExitCode(err) != code || out.String() != "tests\n" {
				t.Fatalf("error=%v, output=%q", err, out.String())
			}
			fake.AssertDone(t)
		})
	}
	for _, target := range []string{"web", "e2e", "contract"} {
		o := Options{Web: target == "web", E2E: target == "e2e", Contract: target == "contract"}
		if err := Run(context.Background(), o, &exec.Fake{}); err == nil || !strings.Contains(err.Error(), "M4") {
			t.Fatalf("%s: %v", target, err)
		}
	}
}
