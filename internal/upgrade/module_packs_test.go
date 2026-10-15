package upgrade

import (
	"context"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/templates"
)

func TestModulePackSelections(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lock := planLock("old\n", "scaffold")
	writePlanFile(t, root, "file.txt", "user\n", 0644)
	old := planVersion("0.1.0", planSpec{path: "file.txt", data: "old\n"})
	// Each side can supply a complete pack filesystem, including plugin packs.
	extra := fstest.MapFS{
		"addon/pack.toml":       {Data: []byte("name = 'addon'\n[[files]]\npath = 'added.txt'\nsource = 'files/added.txt'\nclass = 'managed'\n")},
		"addon/files/added.txt": {Data: []byte("added\n")},
	}
	for name, file := range old.Packs.(fstest.MapFS) {
		extra[name] = file
	}
	next := &templates.Version{Version: "0.1.0", Packs: old.Packs}
	target := PackSet{Names: []string{"base", "addon"}, FS: extra}
	plan, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: lock, Old: old, New: next, NewPacks: &target, Merger: &planMerger{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 2 || plan.Files[0].Path != "added.txt" || plan.Files[0].Action != ActionCreate || plan.Files[1].Action != ActionMerge || string(plan.Files[1].Data) != "user\n" {
		t.Fatalf("wrong plan: %+v", plan.Files)
	}
	saved, err := NextLock(lock, plan, next, nil, target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.Packs, target.Names) || saved.Files["added.txt"].Template == "" || len(lock.Packs) != 1 {
		t.Fatalf("lock: %+v", saved)
	}
	// OLD can have its own plugin filesystem and selection independently of NEW.
	writePlanFile(t, root, "added.txt", "added\n", 0644)
	reverse, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: saved, Old: old, New: next, OldPacks: &target, NewPacks: &PackSet{Names: lock.Packs}, Merger: &planMerger{}})
	if err != nil {
		t.Fatal(err)
	}
	if reverse.Files[0].Action != ActionDelete {
		t.Fatalf("reverse: %+v", reverse.Files)
	}
}
