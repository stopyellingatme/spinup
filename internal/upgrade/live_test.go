package upgrade

import (
	"bytes"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/modules"
)

func TestLiveModuleAddNextSteps(t *testing.T) {
	t.Parallel()
	r, _ := moduleFixture(t, false)
	version := r.Source.(chainSource)["0.1.0"]
	fsys := version.Packs.(fstest.MapFS)
	fsys["live/pack.toml"] = &fstest.MapFile{Data: []byte("name='live'\nrequires=['api']\n[[files]]\npath='api/internal/live/live.go'\nsource='live.go'\nclass='scaffold'\n")}
	fsys["live/live.go"] = &fstest.MapFile{Data: []byte("package live\n")}
	if err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"live"}, Options: Options{Yes: true, NoVerify: true}}); err != nil {
		t.Fatal(err)
	}
	out := r.Out.(*bytes.Buffer).String()
	want := strings.ReplaceAll(modules.LiveNextSteps(), "<module>", "example.com/demo")
	if !strings.Contains(out, want) || !strings.Contains(out, "git merge spinup/module-live") {
		t.Fatalf("output=%s", out)
	}
}
