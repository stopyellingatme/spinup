package upgrade

import (
	"bytes"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/modules"
)

func TestAPIKeysModuleAddNextSteps(t *testing.T) {
	t.Parallel()
	r, _ := moduleFixture(t, false)
	version := r.Source.(chainSource)["0.1.0"]
	fsys := version.Packs.(fstest.MapFS)
	fsys["apikeys/pack.toml"] = &fstest.MapFile{Data: []byte("name='apikeys'\nrequires=['api']\n[[files]]\npath='api/internal/apikeys/apikeys.gen.go'\nsource='apikeys.go'\nclass='managed'\n")}
	fsys["apikeys/apikeys.go"] = &fstest.MapFile{Data: []byte("package apikeys\n")}
	if err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"apikeys"}, Options: Options{Yes: true, NoVerify: true}}); err != nil {
		t.Fatal(err)
	}
	out := r.Out.(*bytes.Buffer).String()
	want := strings.ReplaceAll(modules.APIKeysNextSteps(), "<module>", "example.com/demo")
	if !strings.Contains(out, want) || !strings.Contains(out, "git merge spinup/module-apikeys") {
		t.Fatalf("output=%s", out)
	}
}
