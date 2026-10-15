package upgrade

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/plugin/pack"
)

// TestMatrixPlugin crosses two core versions and two approved plugin versions.
// Independent edits merge, overlapping edits conflict, and generators remain owned.
func TestMatrixPlugin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ctx := context.Background()
	root := t.TempDir()
	source := t.TempDir()
	err := filepath.WalkDir("../../testdata/plugins/example", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel("../../testdata/plugins/example", name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		target := filepath.Join(source, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
	if err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, root, "spinup.toml", "[app]\nname='demo'\nmodule='example.com/demo'\n", 0644)
	metadata, err := os.ReadFile(filepath.Join(source, "pack.toml"))
	if err != nil {
		t.Fatal(err)
	}
	metadata = append(metadata, []byte("\n[[files]]\npath='conflict.txt'\nsource='files/gen/conflict.txt'\nclass='scaffold'\n")...)
	writePlanFile(t, source, "pack.toml", string(metadata), 0644)
	original := "plugin one\n2\n3\n4\n5\n6\n7\n8\nuser area\n"
	writePlanFile(t, source, "files/example.txt.tmpl", original, 0644)
	writePlanFile(t, source, "files/gen/conflict.txt", "original\n", 0644)
	old := planVersion("0.1.0", planSpec{path: "core.txt", data: "core one\n2\n3\n4\n5\n6\n7\n8\nuser area\n"})
	newer := planVersion("0.2.0", planSpec{path: "core.txt", data: "core two\n2\n3\n4\n5\n6\n7\n8\nuser area\n"})
	lock := manifest.New()
	lock.TemplateVersion = old.Version
	lock.Packs = []string{"base"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	manager := &pack.Manager{Root: root, Trust: true, CLIVersion: "dev", Out: io.Discard}
	if err := manager.Add(ctx, "path:"+source, ""); err != nil {
		t.Fatal(err)
	}
	lock, err = manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Root: root, Source: chainSource{old.Version: old, newer.Version: newer}, Out: io.Discard, Merger: NewMerger(execx.OSRunner{})}
	oldSet, newSet, err := r.pluginPacks(ctx, lock, old, old, PackSet{}, false)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(ctx, PlanInput{Root: root, Lock: lock, Old: old, New: old, OldAnswers: lock.Answers, NewAnswers: lock.Answers, OldPacks: &oldSet, NewPacks: &newSet, Merger: r.Merger})
	if err != nil {
		t.Fatal(err)
	}
	if err := writePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	lock, err = NextLock(lock, plan, old, lock.Answers, newSet)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Files["example.txt"].Template != "plugin/example/files/example.txt.tmpl" {
		t.Fatal("missing plugin template prefix")
	}
	writePlanFile(t, root, "greetings/Ada.txt", "Hello, Ada!\n", 0644)
	lock.Files["greetings/Ada.txt"] = manifest.FileEntry{Class: manifest.Scaffold, Template: "plugin/example/gen/greeting", Hash: manifest.Hash([]byte("Hello, Ada!\n")), Mode: "0644"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, root, "example.txt", strings.ReplaceAll(original, "user area", "user edit"), 0644)
	writePlanFile(t, root, "core.txt", "core one\n2\n3\n4\n5\n6\n7\n8\nuser edit\n", 0644)
	writePlanFile(t, root, "conflict.txt", "user conflict\n", 0644)
	pluginMeta, err := os.ReadFile(filepath.Join(source, "plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, source, "plugin.toml", strings.ReplaceAll(string(pluginMeta), "1.0.0", "2.0.0"), 0644)
	writePlanFile(t, source, "files/example.txt.tmpl", strings.ReplaceAll(original, "plugin one", "plugin two"), 0644)
	writePlanFile(t, source, "files/gen/conflict.txt", "plugin conflict\n", 0644)
	if err := manager.Update(ctx, "example", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	// The approval is now v2. OLD must still come from the locked v1 inputs.
	report, err := r.Plan(ctx, Options{To: newer.Version, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Steps) != 1 {
		t.Fatalf("steps=%d", len(report.Steps))
	}
	plan = report.Steps[0]
	if got := plan.Conflicts(); !reflect.DeepEqual(got, []string{"conflict.txt"}) {
		t.Fatalf("conflicts=%v", got)
	}
	for _, file := range plan.Files {
		switch file.Path {
		case "example.txt":
			if file.Action != ActionMerge || !strings.Contains(string(file.Data), "plugin two") || !strings.Contains(string(file.Data), "user edit") {
				t.Fatalf("plugin merge=%+v", file)
			}
		case "core.txt":
			if file.Action != ActionMerge || !strings.Contains(string(file.Data), "core two") || !strings.Contains(string(file.Data), "user edit") {
				t.Fatalf("core merge=%+v", file)
			}
		case "greetings/Ada.txt":
			if file.Action != ActionKeep || file.Reason != "generator-owned" {
				t.Fatalf("generator=%+v", file)
			}
		}
	}
	// Equal core versions must still plan an approved plugin update.
	same, err := r.Plan(ctx, Options{To: old.Version, Offline: true})
	if err != nil || len(same.Steps) != 1 {
		t.Fatalf("same core plan=%v err=%v", same, err)
	}
	current, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	_, updated, err := r.pluginPacks(ctx, current, old, newer, PackSet{}, true)
	if err != nil {
		t.Fatal(err)
	}
	next, err := NextLock(current, plan, newer, current.Answers, updated)
	if err != nil || len(next.Plugins) != 1 || next.Plugins[0].Version != "2.0.0" {
		t.Fatalf("NEW plugins=%v error=%v", next, err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(cache, "spinup/plugins/example/1.0.0")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Plan(ctx, Options{To: newer.Version, Offline: true}); err == nil || !strings.Contains(err.Error(), "Try:") || !strings.Contains(err.Error(), "1.0.0") {
		t.Fatalf("missing OLD err=%v", err)
	}
	if len(lock.Plugins) != 1 || lock.Plugins[0].Version != "1.0.0" {
		t.Fatalf("rendered plugins=%v", lock.Plugins)
	}
}

func TestModuleAddWithPlugin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	r, root := moduleFixture(t, false)
	source, err := filepath.Abs("../../testdata/plugins/example")
	if err != nil {
		t.Fatal(err)
	}
	manager := &pack.Manager{Root: root, Trust: true, CLIVersion: "dev"}
	if err := manager.Add(t.Context(), "path:"+source, ""); err != nil {
		t.Fatal(err)
	}
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	v := r.Source.(chainSource)[lock.TemplateVersion]
	oldPacks, newPacks, err := r.pluginPacks(t.Context(), lock, v, v, PackSet{}, true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(t.Context(), PlanInput{Root: root, Lock: lock, Old: v, New: v, OldAnswers: lock.Answers, NewAnswers: lock.Answers, OldPacks: &oldPacks, NewPacks: &newPacks, Merger: r.Merger})
	if err != nil {
		t.Fatal(err)
	}
	if err := writePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	lock, err = NextLock(lock, plan, v, lock.Answers, newPacks)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, root, ".gitignore", ".spinup/*\n!.spinup/plugins.allow\n!.spinup/plugins.history\n", 0644)
	testGit(t, root, "add", "-A")
	testGit(t, root, "commit", "-m", "render plugin")
	if err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"jobs"}, Options: Options{Yes: true, NoVerify: true, Offline: true}}); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, ".spinup/upgrade/0.1.0")
	for _, name := range []string{"example.txt", "job.txt"} {
		if _, err := os.Stat(filepath.Join(work, name)); err != nil {
			t.Fatal(err)
		}
	}
	next, err := manifest.Load(filepath.Join(work, "spinup.lock"))
	if err != nil || len(next.Plugins) != 1 || next.Plugins[0] != lock.Plugins[0] {
		t.Fatalf("plugins=%v error=%v", next, err)
	}
}

func TestPluginChainUsesPluginSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	writePlanFile(t, root, "spinup.toml", "[app]\nname='demo'\nmodule='example.com/demo'\n", 0644)
	lock := manifest.New()
	lock.TemplateVersion = "0.1.0"
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	manager := &pack.Manager{Root: root, Trust: true, CLIVersion: "dev"}
	for _, name := range []string{"beta", "alpha"} {
		source := t.TempDir()
		writePlanFile(t, source, "plugin.toml", "name='"+name+"'\nversion='1.0.0'\napi=1\nspinup='*'\n", 0644)
		writePlanFile(t, source, "pack.toml", "name='"+name+"'\n", 0644)
		if err := manager.Add(t.Context(), "path:"+source, ""); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := plugin.Resolve(t.Context(), plugin.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range loaded {
		lock.Plugins = append(lock.Plugins, manifest.Plugin{Name: p.Meta.Name, Version: p.Meta.Version, API: p.Meta.API, SHA256: p.SHA256})
	}
	// Saving the lock sorts plugin identities independently of declaration order.
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	lock, err = manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Root: root}
	versions, err := r.pluginChain(t.Context(), lock, nil, true)
	if err != nil || len(versions) != 0 {
		t.Fatalf("unchanged plugin set: versions=%v error=%v", versions, err)
	}
}
