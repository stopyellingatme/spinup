package upgrade

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/templates"
)

const uiButton = "web/src/lib/components/ui/button/button.svelte"

func uiVersion(version, registry, content string) *templates.Version {
	metadata := fmt.Sprintf(`name = "web"
[[questions]]
key = "shadcn_registry"
type = "string"
prompt = "Registry version"
default = %q
[[files]]
path = %q
source = "button.svelte"
class = "seed"
`, registry, uiButton)
	return &templates.Version{Version: version, Packs: fstest.MapFS{
		"web/pack.toml":     {Data: []byte(metadata)},
		"web/button.svelte": {Data: []byte(content)},
	}}
}

func TestUIComponents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, ours, want, old, next string
		enabled, deleted, conflict  bool
	}{
		{name: "opt out", ours: "old\n", want: "old\n"},
		{name: "unchanged", ours: "old\n", want: "new\n", enabled: true},
		{name: "clean merge", ours: "user\nline2\nline3\nold\n", old: "old\nline2\nline3\nold\n", next: "old\nline2\nline3\nnew\n", want: "user\nline2\nline3\nnew\n", enabled: true},
		{name: "already new", ours: "new\n", want: "new\n", enabled: true},
		{name: "deleted", deleted: true, enabled: true},
		{name: "conflict", ours: "user\n", want: "<<<<<<<", enabled: true, conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if !tc.deleted {
				writePlanFile(t, root, uiButton, tc.ours, 0644)
			}
			lock := manifest.New()
			lock.Packs = []string{"web"}
			lock.Answers["shadcn_registry"] = "1.7.0"
			lock.Files[uiButton] = manifest.FileEntry{Class: manifest.Seed}
			out := &bytes.Buffer{}
			old, next := tc.old, tc.next
			if old == "" {
				old = "old\n"
			}
			if next == "" {
				next = "new\n"
			}
			s := &Step{From: uiVersion("0.1.0", "1.7.0", old), To: uiVersion("0.2.0", "1.8.0", next), Worktree: root, Lock: lock, Answers: clonePlanAnswers(lock.Answers), Plan: &Plan{}, Out: out}
			err := runUI(context.Background(), s, tc.enabled, NewMerger(exec.OSRunner{}))
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if (len(s.Conflicts) > 0) != tc.conflict {
				t.Fatalf("conflicts: %v, want conflict %v", s.Conflicts, tc.conflict)
			}
			data, readErr := os.ReadFile(filepath.Join(root, uiButton))
			if tc.deleted {
				if !os.IsNotExist(readErr) {
					t.Fatalf("deleted file: %v", readErr)
				}
			} else if readErr != nil || !strings.Contains(string(data), tc.want) {
				t.Fatalf("file: %q, %v", data, readErr)
			}
			if tc.enabled {
				if lock.Answers["shadcn_registry"] != "1.8.0" {
					t.Fatal(lock.Answers)
				}
				if (len(s.Plan.Conflicts()) > 0) != tc.conflict {
					t.Fatal(s.Plan.Conflicts())
				}
			} else if strings.Count(out.String(), "--ui") != 1 || lock.Answers["shadcn_registry"] != "1.7.0" {
				t.Fatalf("note: %s, answers: %v", out, lock.Answers)
			}
		})
	}
}

func TestUINewAndRetainedComponents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                string
		absent, tombstone, managed, outside bool
	}{
		{name: "new", absent: true},
		{name: "tombstone", absent: true, tombstone: true},
		{name: "old managed", managed: true},
		{name: "outside UI", outside: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			old := uiVersion("0.1.0", "1.7.0", "old\n")
			next := uiVersion("0.2.0", "1.8.0", "new\n")
			if tc.absent {
				files := old.Packs.(fstest.MapFS)
				metadata := string(files["web/pack.toml"].Data)
				metadata, _, _ = strings.Cut(metadata, "[[files]]")
				files["web/pack.toml"].Data = []byte(metadata)
			}
			if tc.managed {
				old.Packs.(fstest.MapFS)["web/pack.toml"].Data = bytes.ReplaceAll(old.Packs.(fstest.MapFS)["web/pack.toml"].Data, []byte(`class = "seed"`), []byte(`class = "managed"`))
			}
			name := uiButton
			if tc.outside {
				name = "web/src/lib/api/schema.d.ts"
				for _, v := range []*templates.Version{old, next} {
					v.Packs.(fstest.MapFS)["web/pack.toml"].Data = bytes.ReplaceAll(v.Packs.(fstest.MapFS)["web/pack.toml"].Data, []byte(uiButton), []byte(name))
				}
			}
			lock := manifest.New()
			lock.Packs = []string{"web"}
			lock.Answers["shadcn_registry"] = "1.7.0"
			if tc.tombstone {
				lock.Tombstones = []string{name}
			}
			if !tc.absent {
				writePlanFile(t, root, name, "old\n", 0644)
			}
			s := &Step{From: old, To: next, Worktree: root, Lock: lock, Answers: clonePlanAnswers(lock.Answers), Plan: &Plan{}}
			if err := runUI(context.Background(), s, true, NewMerger(exec.OSRunner{})); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, name))
			switch {
			case tc.tombstone:
				if !os.IsNotExist(err) {
					t.Fatalf("tombstone recreated: %q, %v", data, err)
				}
			case tc.absent:
				if err != nil || string(data) != "new\n" {
					t.Fatalf("new component: %q, %v", data, err)
				}
				if lock.Files[name].Hash != manifest.Hash([]byte("new\n")) || lock.Files[name].Class != manifest.Seed {
					t.Fatal(lock.Files[name])
				}
			default:
				if err != nil || string(data) != "old\n" {
					t.Fatalf("retained file: %q, %v", data, err)
				}
			}
		})
	}
}
