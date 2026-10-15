package upgrade

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	pluginrender "github.com/stopyellingatme/spinup/internal/plugin/render"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func pluginRegressionVersion(t *testing.T, value string) (*templates.Version, PackSet) {
	t.Helper()
	base := planVersion("0.1.0")
	v, names, err := pluginrender.Overlay(base, []string{"base"}, []plugin.Loaded{{Meta: plugin.Meta{Name: "example", Version: "1.0.0", API: 1}, SHA256: strings.Repeat("a", 64), FS: fstest.MapFS{
		"pack.toml":     {Data: []byte("name='example'\n[[questions]]\nkey='word'\ntype='string'\ndefault='" + value + "'\n[[files]]\npath='file.txt'\nsource='gen/file.tmpl'\nclass='managed'\n")},
		"gen/file.tmpl": {Data: []byte("[[ .word ]]\n")},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return v, PackSet{FS: v.Packs, Names: names}
}

func TestPluginSourceGenOwnership(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"update", "remove-unmodified", "remove-edited", "legacy-generator", "generator"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			old, selection := pluginRegressionVersion(t, "one")
			lock := manifest.New()
			lock.TemplateVersion = old.Version
			lock.Packs = selection.Names
			lock.Files["file.txt"] = manifest.FileEntry{Class: manifest.Managed, Template: "plugin/example/gen/file.tmpl", Hash: manifest.Hash([]byte("one\n")), Mode: "0644"}
			data := "one\n"
			newer, newSelection := pluginRegressionVersion(t, "two")
			want := ActionOverwrite
			if strings.HasPrefix(scenario, "remove") {
				newer = planVersion("0.1.0")
				newSelection = PackSet{Names: []string{"base"}}
				want = ActionDelete
			}
			if scenario == "remove-edited" {
				data = "user\n"
				want = ActionOrphan
			}
			if scenario == "legacy-generator" || scenario == "generator" {
				lock.Files["file.txt"] = manifest.FileEntry{Class: manifest.Managed, Template: "plugin/example/gen/greeting", Hash: manifest.Hash([]byte(data)), Mode: "0644"}
				if scenario == "generator" {
					e := lock.Files["file.txt"]
					e.Template = "plugin/example/@gen/greeting"
					lock.Files["file.txt"] = e
				}
				want = ActionKeep
			}
			writePlanFile(t, root, "file.txt", data, 0644)
			p, err := BuildPlan(t.Context(), PlanInput{Root: root, Lock: lock, Old: old, New: newer, OldPacks: &selection, NewPacks: &newSelection})
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Files) != 1 || p.Files[0].Action != want {
				t.Fatalf("decisions=%+v want=%s", p.Files, want)
			}
		})
	}
}

func TestNextLockCompletesPluginAnswers(t *testing.T) {
	t.Parallel()
	v, selection := pluginRegressionVersion(t, "one")
	lock := manifest.New()
	lock.TemplateVersion = v.Version
	lock.Packs = []string{"base"}
	answers := map[string]any{}
	next, err := NextLock(lock, &Plan{}, v, answers, selection)
	if err != nil {
		t.Fatal(err)
	}
	if next.Answers["word"] != "one" {
		t.Fatalf("answers=%v", next.Answers)
	}
	if len(answers) != 0 {
		t.Fatal("modified caller answers")
	}
	newer, names := pluginRegressionVersion(t, "two")
	rendered, err := Render(newer, names.Names, next.Answers)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := rendered.Get("file.txt")
	if string(file.Data) != "one\n" {
		t.Fatalf("changed default changed render: %q", file.Data)
	}
}
