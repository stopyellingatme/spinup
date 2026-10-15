package templates_test

import (
	"io/fs"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenCi(t *testing.T) {
	if !testutil.GoldenSelected(t, "ci") {
		return
	}
	t.Parallel()

	render := func() (fs.FS, error) {
		return testutil.RenderPack(t, []string{"ci"}, nil), nil
	}
	fsys := testutil.RenderPack(t, []string{"ci"}, nil)
	testutil.Golden(t, "ci", fsys)
	testutil.Deterministic(t, render)
}
