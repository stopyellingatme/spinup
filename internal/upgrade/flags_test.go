package upgrade

import (
	"bytes"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/modules"
)

func TestFlagsModuleAddNextSteps(t *testing.T) {
	t.Parallel()
	r, _ := moduleFixture(t, false)
	version := r.Source.(chainSource)["0.1.0"]
	fsys := version.Packs.(fstest.MapFS)
	fsys["flags/pack.toml"] = &fstest.MapFile{Data: []byte("name='flags'\nrequires=['api']\n[[files]]\npath='api/internal/flags/flags.gen.go'\nsource='flags.go'\nclass='managed'\n")}
	fsys["flags/flags.go"] = &fstest.MapFile{Data: []byte("package flags\n")}
	if err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"flags"}, Options: Options{Yes: true, NoVerify: true}}); err != nil {
		t.Fatal(err)
	}
	out := r.Out.(*bytes.Buffer).String()
	want := strings.ReplaceAll(modules.FlagsNextSteps(), "<module>", "example.com/demo")
	if !strings.Contains(out, want) || !strings.Contains(out, "git merge spinup/module-flags") {
		t.Fatalf("output=%s", out)
	}
}
