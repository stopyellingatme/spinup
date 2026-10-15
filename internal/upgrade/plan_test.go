package upgrade

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/merge"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

type planMerger struct {
	inputs []merge.Input
	err    error
}

func (m *planMerger) Merge(_ context.Context, in merge.Input) (merge.Result, error) {
	m.inputs = append(m.inputs, in)
	if m.err != nil {
		return merge.Result{}, m.err
	}
	switch {
	case bytes.Equal(in.Ours, in.Base):
		return merge.Result{Data: in.Theirs}, nil
	case bytes.Equal(in.Theirs, in.Base):
		return merge.Result{Data: in.Ours}, nil
	default:
		return merge.Result{Data: []byte("<<<<<<< ours\n" + string(in.Ours) + "=======\n" + string(in.Theirs) + ">>>>>>> theirs\n"), Conflicted: true}, nil
	}
}

func planLock(data, class string) *manifest.Lock {
	lock := manifest.New()
	lock.TemplateVersion = "0.1.0"
	lock.Packs = []string{"base"}
	if class != "" {
		lock.Files["file.txt"] = manifest.FileEntry{Class: manifest.Class(class), Template: "base/file0.tmpl", Hash: manifest.Hash([]byte(data)), Mode: "0644"}
	}
	return lock
}
func writePlanFile(t *testing.T, root, name, data string, mode fs.FileMode) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(full, mode); err != nil {
		t.Fatal(err)
	}
}

func TestPlanDecisions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, class, nextClass, disk, next, base, reason     string
		action                                               Action
		missing, removed, untracked, tombstone, noOld, force bool
		mode                                                 fs.FileMode
		merges                                               int
	}{
		{name: "tombstone", tombstone: true, action: ActionKeep, reason: "tombstone"},
		{name: "missing scaffold", class: "scaffold", missing: true, action: ActionKeep},
		{name: "missing managed", class: "managed", missing: true, action: ActionKeep},
		{name: "seed", class: "seed", disk: "user", action: ActionKeep},
		{name: "new seed free", nextClass: "seed", untracked: true, missing: true, action: ActionCreate},
		{name: "new seed occupied", nextClass: "seed", untracked: true, disk: "user", action: ActionKeep},
		{name: "becomes seed", class: "managed", nextClass: "seed", disk: "user", action: ActionKeep},
		{name: "removed unchanged", class: "scaffold", disk: "old", removed: true, action: ActionDelete},
		{name: "removed changed", class: "managed", disk: "user", removed: true, action: ActionOrphan},
		{name: "new free", untracked: true, missing: true, action: ActionCreate},
		{name: "adopt identical", untracked: true, disk: "new", action: ActionUnchanged},
		{name: "new occupied", untracked: true, disk: "user", action: ActionConflict, reason: "path exists", merges: 1},
		{name: "identical", class: "managed", disk: "new", action: ActionUnchanged},
		{name: "mode", class: "scaffold", disk: "new", mode: 0o755, action: ActionOverwrite, reason: "mode"},
		{name: "unchanged disk", class: "managed", disk: "old", action: ActionOverwrite},
		{name: "CRLF", class: "scaffold", disk: "old\r\n\r\n", action: ActionOverwrite},
		{name: "binary ours", class: "managed", disk: "user\x00", action: ActionKeep, reason: "binary or large file changed on disk"},
		{name: "binary theirs", class: "scaffold", disk: "user", next: "new\x00", action: ActionKeep, reason: "binary or large file changed on disk"},
		{name: "binary base", class: "scaffold", disk: "user", base: "old\x00", action: ActionKeep, reason: "binary or large file changed on disk"},
		{name: "large ours", class: "managed", disk: strings.Repeat("u", merge.MaxSize+1), force: true, action: ActionKeep, reason: "binary or large file changed on disk"},
		{name: "large theirs", class: "scaffold", disk: "user", next: strings.Repeat("n", merge.MaxSize+1), action: ActionKeep},
		{name: "large base", class: "scaffold", disk: "user", base: strings.Repeat("b", merge.MaxSize+1), action: ActionKeep},
		{name: "forced managed", class: "managed", disk: "user", force: true, action: ActionOverwrite, reason: "forced"},
		{name: "ejected not forced", class: "scaffold", nextClass: "managed", disk: "user", force: true, action: ActionConflict, merges: 1},
		{name: "scaffold not forced", class: "scaffold", disk: "user", force: true, action: ActionConflict, merges: 1},
		{name: "managed conflict", class: "managed", disk: "user", action: ActionConflict, merges: 1},
		{name: "scaffold clean", class: "scaffold", disk: "user", next: "old", action: ActionMerge, merges: 1},
		{name: "managed clean", class: "managed", disk: "user", next: "old", action: ActionMerge, merges: 1},
		{name: "no base changed", class: "scaffold", disk: "user", noOld: true, action: ActionConflict, reason: "no base version", merges: 1},
		{name: "no base unchanged", class: "managed", disk: "old", noOld: true, action: ActionOverwrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			lock := planLock("old", tc.class)
			if tc.tombstone {
				lock.Tombstone("file.txt")
			}
			if !tc.missing {
				writePlanFile(t, root, "file.txt", tc.disk, 0o644)
			}
			next, base := tc.next, tc.base
			if next == "" {
				next = "new"
			}
			if base == "" {
				base = "old"
			}
			old := planVersion("0.1.0", planSpec{path: "file.txt", data: base})
			if tc.noOld {
				old = nil
			}
			nextClass := tc.nextClass
			if nextClass == "" {
				nextClass = tc.class
			}
			newer := planVersion("0.2.0", planSpec{path: "file.txt", data: next, class: nextClass, mode: tc.mode})
			if tc.removed {
				newer = planVersion("0.2.0")
			}
			merger := &planMerger{}
			p, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: lock, Old: old, New: newer, Merger: merger, ForceManaged: tc.force})
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Files) != 1 {
				t.Fatalf("files: %#v", p.Files)
			}
			d := p.Files[0]
			if d.Action != tc.action || (tc.reason != "" && d.Reason != tc.reason) {
				t.Fatalf("decision: %#v", d)
			}
			if len(merger.inputs) != tc.merges {
				t.Fatalf("merge count: %d", len(merger.inputs))
			}
			if tc.missing && tc.class != "" {
				if !reflect.DeepEqual(p.Tombstoned, []string{"file.txt"}) {
					t.Fatalf("tombstones: %v", p.Tombstoned)
				}
				if tc.class == "managed" && !reflect.DeepEqual(p.MissingManaged, []string{"file.txt"}) {
					t.Fatal(p.MissingManaged)
				}
			}
			nextLock, err := NextLock(lock, p, newer, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.action == ActionOrphan && !reflect.DeepEqual(nextLock.Orphans, []string{"file.txt"}) {
				t.Fatal(nextLock.Orphans)
			}
			if tc.action == ActionDelete || tc.action == ActionOrphan || tc.tombstone || (tc.missing && tc.class != "") {
				if _, ok := nextLock.Files["file.txt"]; ok {
					t.Fatal("retained removed entry")
				}
			} else if nextLock.Files["file.txt"].Class != manifest.Seed && nextLock.Files["file.txt"].Hash != manifest.Hash([]byte(next)) {
				t.Fatal("lock does not hold NEW")
			}
		})
	}
}

func TestPlanRename(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, disk, next string
		action           Action
	}{
		{"unchanged", "old", "new", ActionRename},
		{"identical destination", "old", "new", ActionRename},
		{"edited clean", "user", "old", ActionRename},
		{"edited conflict", "user", "new", ActionConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writePlanFile(t, root, "file.txt", tc.disk, 0o644)
			if tc.name == "identical destination" {
				writePlanFile(t, root, "moved.txt", tc.next, 0o644)
			}
			lock := planLock("old", "scaffold")
			newer := planVersion("0.2.0", planSpec{path: "moved.txt", data: tc.next, from: "file.txt", mode: 0o755})
			p, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: lock, Old: planVersion("0.1.0", planSpec{path: "file.txt", data: "old"}), New: newer, Merger: &planMerger{}})
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Files) != 1 || p.Files[0].Action != tc.action || p.Files[0].From != "file.txt" || p.Files[0].Mode != 0o755 {
				t.Fatalf("plan: %#v", p)
			}
			next, err := NextLock(lock, p, newer, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(next.Files) != 1 || next.Files["moved.txt"].Hash != manifest.Hash([]byte(tc.next)) {
				t.Fatal(next.Files)
			}
		})
	}
}

func TestPlanReadErrors(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"escape", "symlink", "directory", "merge", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			lock := planLock("old", "managed")
			ctx := context.Background()
			merger := &planMerger{}
			switch kind {
			case "escape":
				lock.Files["../escape"] = lock.Files["file.txt"]
			case "symlink":
				outside := t.TempDir()
				writePlanFile(t, outside, "file.txt", "outside", 0o644)
				if err := os.Symlink(filepath.Join(outside, "file.txt"), filepath.Join(root, "file.txt")); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(filepath.Join(root, "file.txt"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "merge":
				writePlanFile(t, root, "file.txt", "user", 0o644)
				merger.err = errors.New("failed")
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := BuildPlan(ctx, PlanInput{Root: root, Lock: lock, New: planVersion("0.2.0", planSpec{path: "file.txt", data: "new"}), Merger: merger})
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestPlanFormatAndSorting(t *testing.T) {
	t.Parallel()
	p := &Plan{From: "0.1.0", To: "0.2.0", Files: []FileDecision{
		{Path: "unchanged.txt", Action: ActionUnchanged},
		{Path: "z.txt", Action: ActionConflict, Reason: "no base version"},
		{Path: "keep.txt", Action: ActionKeep, Reason: "seed"},
		{Path: "orphan.txt", Action: ActionOrphan},
		{Path: "delete.txt", Action: ActionDelete},
		{Path: "rename.txt", From: "old.txt", Action: ActionRename},
		{Path: "create.txt", Action: ActionCreate},
		{Path: "overwrite.txt", Action: ActionOverwrite},
		{Path: "merge.txt", Action: ActionMerge},
		{Path: "a.txt", Action: ActionConflict},
	}}
	var out bytes.Buffer
	if err := p.Format(&out); err != nil {
		t.Fatal(err)
	}
	want := "Upgrade 0.1.0 -> 0.2.0\nconflict: 2\n  a.txt\n  z.txt (no base version)\nmerge: 1\n  merge.txt\noverwrite: 1\n  overwrite.txt\ncreate: 1\n  create.txt\nrename: 1\n  old.txt -> rename.txt\ndelete: 1\n  delete.txt\norphan: 1\n  orphan.txt\nkeep: 1\n  keep.txt (seed)\nunchanged: 1\n"
	if out.String() != want {
		t.Fatalf("format:\n%s\nwant:\n%s", out.String(), want)
	}
	if !reflect.DeepEqual(p.Conflicts(), []string{"a.txt", "z.txt"}) {
		t.Fatal(p.Conflicts())
	}
	if !reflect.DeepEqual(p.Summary()[ActionKeep], []string{"keep.txt"}) {
		t.Fatal(p.Summary())
	}
	if p.Files[0].Path != "unchanged.txt" {
		t.Fatal("Format mutated plan")
	}
	if err := p.Format(planErrorWriter{}); err == nil {
		t.Fatal("writer error ignored")
	}
	root := t.TempDir()
	newer := planVersion("0.2.0", planSpec{path: "z", data: "z"}, planSpec{path: "a", data: "a"})
	built, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: planLock("", ""), New: newer})
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Files) != 2 || built.Files[0].Path != "a" || built.Files[1].Path != "z" {
		t.Fatalf("files: %v", built.Files)
	}
}

type planErrorWriter struct{}

func (planErrorWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestNextLockPreservesEditsAcrossSteps(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// The first upgrade has no template change. The merger retains the user's edit.
	writePlanFile(t, root, "file.txt", "user edit", 0o644)
	lock := planLock("old", "scaffold")
	old := planVersion("0.1.0", planSpec{path: "file.txt", data: "old"})
	middle := planVersion("0.2.0", planSpec{path: "file.txt", data: "old"})
	first, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: lock, Old: old, New: middle, Merger: &planMerger{}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Files[0].Action != ActionMerge || string(first.Files[0].Data) != "user edit\n" {
		t.Fatalf("first: %v", first.Files)
	}
	answers := map[string]any{"modules": []string{"ci"}}
	next, err := NextLock(lock, first, middle, answers)
	if err != nil {
		t.Fatal(err)
	}
	if next.Files["file.txt"].Hash != manifest.Hash([]byte("old")) {
		t.Fatal("lock recorded merged content")
	}
	answers["modules"].([]string)[0] = "changed"
	if next.Answers["modules"].([]string)[0] != "ci" {
		t.Fatal("answers aliased")
	}
	writePlanFile(t, root, "file.txt", string(first.Files[0].Data), first.Files[0].Mode)
	newest := planVersion("0.3.0", planSpec{path: "file.txt", data: "newest"})
	second, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: next, Old: middle, New: newest, Merger: &planMerger{}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Files[0].Action != ActionConflict {
		t.Fatalf("second upgrade would overwrite user edit: %v", second.Files)
	}
	if lock.TemplateVersion != "0.1.0" || lock.Files["file.txt"].Hash != manifest.Hash([]byte("old")) {
		t.Fatal("old lock changed")
	}
}

func TestPlanOldRenderMissingPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePlanFile(t, root, "file.txt", "user\r\n", 0o644)
	merger := &planMerger{}
	p, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: planLock("old", "scaffold"), Old: planVersion("0.1.0"), New: planVersion("0.2.0", planSpec{path: "file.txt", data: "new"}), Merger: merger})
	if err != nil {
		t.Fatal(err)
	}
	if p.Files[0].Reason != "no base version" || len(merger.inputs[0].Base) != 0 || string(merger.inputs[0].Ours) != "user\n" {
		t.Fatalf("plan: %v, input: %v", p, merger.inputs)
	}
}

func TestPlanDoesNotWrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePlanFile(t, root, "file.txt", "user", 0o644)
	writePlanFile(t, root, "untracked.txt", "untouched", 0o600)
	lock := planLock("old", "managed")
	before, err := lock.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildPlan(context.Background(), PlanInput{Root: root, Lock: lock, Old: planVersion("0.1.0", planSpec{path: "file.txt", data: "old"}), New: planVersion("0.2.0", planSpec{path: "file.txt", data: "new"}), Merger: &planMerger{}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := lock.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("planner changed the lock")
	}
	for name, want := range map[string]string{"file.txt": "user", "untracked.txt": "untouched"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatalf("file %s: %q, %v", name, data, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries: %v, %v", entries, err)
	}
}

func TestPlanRenameCannotMergeBinary(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"binary", "large"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			disk := strings.Repeat("user", merge.MaxSize)
			if kind == "binary" {
				disk = "user\x00"
			}
			writePlanFile(t, root, "file.txt", disk, 0o644)
			_, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: planLock("old", "managed"), Old: planVersion("0.1.0", planSpec{path: "file.txt", data: "old"}), New: planVersion("0.2.0", planSpec{path: "moved.txt", data: "new", from: "file.txt"}), Merger: &planMerger{}})
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestNextLockSnapshot(t *testing.T) {
	t.Parallel()
	old := planLock("old", "scaffold")
	old.BehaviorVersion = 3
	old.Kit = manifest.KitRef{Module: "example.com/kit", Version: "v0.1.0"}
	old.Orphans = []string{"adopted.txt", "previous.txt"}
	old.Tombstones = []string{"gone.txt"}
	old.AppliedMigrations = []string{"0001"}
	answers := map[string]any{"nested": map[string]any{"items": []any{"original"}}}
	newer := planVersion("0.2.0", planSpec{path: "file.txt", data: "new", mode: 0o755}, planSpec{path: "adopted.txt", data: "adopted"}, planSpec{path: "seed.txt", data: "seed", class: "seed"})
	newer.Stack.Images = map[string]stackversions.Image{
		"second": {Image: "second", Tag: "2", Digest: "sha256:two"},
		"first":  {Image: "first", Tag: "1", Digest: "sha256:one"},
	}
	p, err := BuildPlan(context.Background(), PlanInput{Root: t.TempDir(), Lock: old, New: newer})
	if err != nil {
		t.Fatal(err)
	}
	next, err := NextLock(old, p, newer, answers)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
	if next.TemplateVersion != "0.2.0" || next.BehaviorVersion != 3 || next.Kit != old.Kit || !reflect.DeepEqual(next.Packs, old.Packs) || !reflect.DeepEqual(next.AppliedMigrations, old.AppliedMigrations) {
		t.Fatalf("lock metadata: %#v", next)
	}
	if !reflect.DeepEqual(next.StackVersions, map[string]string{"first": "first:1@sha256:one", "second": "second:2@sha256:two"}) {
		t.Fatal(next.StackVersions)
	}
	if !reflect.DeepEqual(next.Orphans, []string{"previous.txt"}) || !reflect.DeepEqual(next.Tombstones, []string{"file.txt", "gone.txt"}) {
		t.Fatalf("orphans: %v, tombstones: %v", next.Orphans, next.Tombstones)
	}
	if next.Files["seed.txt"] != (manifest.FileEntry{Class: manifest.Seed}) {
		t.Fatal(next.Files["seed.txt"])
	}
	next.Packs[0] = "changed"
	next.AppliedMigrations[0] = "changed"
	next.Answers["nested"].(map[string]any)["items"].([]any)[0] = "changed"
	if old.Packs[0] != "base" || old.AppliedMigrations[0] != "0001" || answers["nested"].(map[string]any)["items"].([]any)[0] != "original" {
		t.Fatal("next lock aliases inputs")
	}
}

func TestPlanRenameOccupiedDestination(t *testing.T) {
	t.Parallel()
	for _, destination := range []string{"aaa.txt", "moved.txt"} {
		t.Run(destination, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writePlanFile(t, root, "file.txt", "old", 0o644)
			writePlanFile(t, root, destination, "USER WORK", 0o600)
			lock := planLock("old", "scaffold")
			newer := planVersion("0.2.0", planSpec{path: destination, data: "new", from: "file.txt"})
			merger := &planMerger{}
			p, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: lock, New: newer, Merger: merger})
			if err != nil {
				t.Fatal(err)
			}
			var conflict FileDecision
			for _, d := range p.Files {
				if d.Path == destination {
					conflict = d
				}
			}
			if conflict.Action != ActionConflict || conflict.Reason != "path exists" || conflict.From != "" {
				t.Fatalf("destination: %#v", conflict)
			}
			if len(merger.inputs) != 1 || len(merger.inputs[0].Base) != 0 || string(merger.inputs[0].Ours) != "USER WORK\n" {
				t.Fatalf("merge inputs: %#v", merger.inputs)
			}
			next, err := NextLock(lock, p, newer, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(next.Orphans, []string{"file.txt"}) || next.Files[destination].Hash != manifest.Hash([]byte("new")) {
				t.Fatalf("next lock: %#v", next)
			}
			if err := writePlan(root, p); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, "file.txt"))
			if err != nil || string(data) != "old" {
				t.Fatalf("rename source: %q, %v", data, err)
			}
			data, err = os.ReadFile(filepath.Join(root, destination))
			if err != nil || !bytes.Contains(data, []byte("USER WORK")) || !bytes.Contains(data, []byte("<<<<<<<")) {
				t.Fatalf("destination content: %q, %v", data, err)
			}
		})
	}
}

func TestPlanEjectedClass(t *testing.T) {
	t.Parallel()
	for _, force := range []bool{false, true} {
		name := "default"
		if force {
			name = "force managed"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writePlanFile(t, root, "file.txt", "user", 0o644)
			lock := planLock("old", "scaffold")
			newer := planVersion("0.2.0", planSpec{path: "file.txt", data: "new", class: "managed"})
			p, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: lock, Old: planVersion("0.1.0", planSpec{path: "file.txt", data: "old", class: "managed"}), New: newer, Merger: &planMerger{}, ForceManaged: force})
			if err != nil {
				t.Fatal(err)
			}
			if p.Files[0].Action != ActionConflict || p.Files[0].Class != manifest.Scaffold {
				t.Errorf("ejected decision: %#v", p.Files[0])
			}
			next, err := NextLock(lock, p, newer, nil)
			if err != nil {
				t.Fatal(err)
			}
			if next.Files["file.txt"].Class != manifest.Scaffold {
				t.Errorf("ejected class reset: %#v", next.Files["file.txt"])
			}
		})
	}
}

func TestPlanClassTransitions(t *testing.T) {
	t.Parallel()
	for _, oldClass := range []manifest.Class{manifest.Managed, manifest.Scaffold, manifest.Seed} {
		for _, newClass := range []manifest.Class{manifest.Managed, manifest.Scaffold, manifest.Seed} {
			t.Run(string(oldClass)+" to "+string(newClass), func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				writePlanFile(t, root, "file.txt", "user", 0o644)
				lock := planLock("old", string(oldClass))
				newer := planVersion("0.2.0", planSpec{path: "file.txt", data: "new", class: string(newClass)})
				p, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: lock, New: newer, Merger: &planMerger{}, ForceManaged: true})
				if err != nil {
					t.Fatal(err)
				}
				wantClass, wantAction := newClass, ActionConflict
				if oldClass == manifest.Scaffold && newClass == manifest.Managed {
					wantClass = manifest.Scaffold
				}
				switch wantClass {
				case manifest.Managed:
					wantAction = ActionOverwrite
				case manifest.Seed:
					wantAction = ActionKeep
				}
				if p.Files[0].Class != wantClass || p.Files[0].Action != wantAction {
					t.Fatalf("decision: %#v", p.Files[0])
				}
				next, err := NextLock(lock, p, newer, nil)
				if err != nil {
					t.Fatal(err)
				}
				if next.Files["file.txt"].Class != wantClass {
					t.Fatalf("next class: %s, want %s", next.Files["file.txt"].Class, wantClass)
				}
			})
		}
	}
}

func TestPlanRenameEjectedClass(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePlanFile(t, root, "file.txt", "old", 0o644)
	lock := planLock("old", "scaffold")
	newer := planVersion("0.2.0", planSpec{path: "moved.txt", data: "new", from: "file.txt", class: "managed"})
	p, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: lock, New: newer, ForceManaged: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Files[0].Class != manifest.Scaffold || p.Files[0].Action != ActionRename {
		t.Fatalf("rename: %#v", p.Files[0])
	}
	next, err := NextLock(lock, p, newer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if next.Files["moved.txt"].Class != manifest.Scaffold {
		t.Fatalf("renamed class: %s", next.Files["moved.txt"].Class)
	}
}
