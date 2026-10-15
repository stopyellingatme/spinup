package upgrade

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/merge"
	"github.com/stopyellingatme/spinup/internal/templates"
)

const uiPrefix = "web/src/lib/components/ui/"

var uiEnabled atomic.Bool

// uiFlag synchronizes access to the boolean extension flag.
type uiFlag struct{}

func (uiFlag) String() string { return fmt.Sprint(uiEnabled.Load()) }
func (uiFlag) Type() string   { return "bool" }
func (uiFlag) Set(value string) error {
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	uiEnabled.Store(enabled)
	return nil
}

func init() {
	RegisterExtension(Extension{
		Name: "ui",
		Flags: func(flags *pflag.FlagSet) {
			uiEnabled.Store(false)
			flags.Var(uiFlag{}, "ui", "merge shadcn-svelte component updates")
			flags.Lookup("ui").NoOptDefVal = "true"
		},
		Run: func(ctx context.Context, s *Step) error {
			merger := s.Merger
			if merger == nil {
				merger = NewMerger(s.Runner)
			}
			return runUI(ctx, s, uiEnabled.Load(), merger)
		},
	})
}

func runUI(ctx context.Context, s *Step, enabled bool, merger merge.Merger) error {
	if !slices.Contains(s.Lock.Packs, "web") {
		return nil
	}
	pack, err := templates.Load(s.To.Packs, "web")
	if err != nil {
		return fmt.Errorf("load UI registry: %w\nTry: restore the target web pack", err)
	}
	registry := ""
	for _, question := range pack.Questions {
		if question.Key == "shadcn_registry" {
			registry = question.Default
			break
		}
	}
	if registry == "" {
		return fmt.Errorf("load UI registry: shadcn_registry has no default\nTry: restore the target web pack")
	}
	if !enabled {
		if s.Lock.Answers["shadcn_registry"] != registry {
			return phasePrint(s, "UI components use registry %v. Registry %s is available. Try: spinup upgrade --ui\n", s.Lock.Answers["shadcn_registry"], registry)
		}
		return nil
	}
	if s.From == nil {
		return fmt.Errorf("update UI components: the old template version is unavailable\nTry: restore the old templates with spinup cache warm")
	}
	old, err := Render(s.From, []string{"web"}, s.Lock.Answers)
	if err != nil {
		return err
	}
	answers := clonePlanAnswers(s.Answers)
	answers["shadcn_registry"] = registry
	newer, err := Render(s.To, []string{"web"}, answers)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(s.Worktree)
	if err != nil {
		return fmt.Errorf("open UI worktree: %w\nTry: restore the upgrade worktree", err)
	}
	defer root.Close()
	updates := &Plan{}
	for _, file := range newer.Sorted().Files {
		if !strings.HasPrefix(file.Path, uiPrefix) || file.Class != string(manifest.Seed) {
			continue
		}
		base, existed := old.Get(file.Path)
		if existed && base.Class != string(manifest.Seed) {
			continue
		}
		if slices.Contains(s.Lock.Tombstones, file.Path) {
			continue
		}
		if err := planContext(ctx); err != nil {
			return err
		}
		disk, err := readPlanDisk(root, file.Path)
		if err != nil {
			return err
		}
		if existed && !disk.exists {
			continue
		}
		d := FileDecision{Path: file.Path, Class: manifest.Seed, Template: file.Template, Mode: file.Mode, Data: file.Data, Action: ActionOverwrite}
		switch {
		case !disk.exists:
			d.Action = ActionCreate
		case bytes.Equal(manifest.Normalize(disk.data), manifest.Normalize(file.Data)):
			d.Action = ActionUnchanged
		case existed && bytes.Equal(manifest.Normalize(disk.data), manifest.Normalize(base.Data)):
		default:
			d, err = mergePlanFile(ctx, merger, d, manifest.Normalize(base.Data), manifest.Normalize(disk.data), manifest.Normalize(file.Data), "UI component update")
			if err != nil {
				return err
			}
		}
		updates.Files = append(updates.Files, d)
	}
	if err := writePlan(s.Worktree, updates); err != nil {
		return err
	}
	for _, d := range updates.Files {
		file, _ := newer.Get(d.Path)
		s.Lock.Files[d.Path] = manifest.FileEntry{Class: manifest.Seed, Template: file.Template, Hash: manifest.Hash(file.Data), Mode: fmt.Sprintf("%04o", file.Mode.Perm())}
		index := slices.IndexFunc(s.Plan.Files, func(previous FileDecision) bool { return previous.Path == d.Path })
		if index >= 0 {
			s.Plan.Files[index] = d
		} else {
			s.Plan.Files = append(s.Plan.Files, d)
		}
	}
	slices.SortFunc(s.Plan.Files, func(a, b FileDecision) int { return strings.Compare(a.Path, b.Path) })
	s.Lock.Answers["shadcn_registry"] = registry
	s.Answers["shadcn_registry"] = registry
	// The state machine stops on these like plan conflicts (exit 3), and --continue
	// resumes at the lock commit.
	s.Conflicts = append(s.Conflicts, updates.Conflicts()...)
	return nil
}
