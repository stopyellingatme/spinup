package upgrade

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/apikeys"
	"github.com/stopyellingatme/spinup/internal/gen/billing"
	"github.com/stopyellingatme/spinup/internal/gen/flags"
	"github.com/stopyellingatme/spinup/internal/gen/mcp"
	"github.com/stopyellingatme/spinup/internal/gen/roles"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

var generatedFiles = []struct{ path, pack string }{
	{project.BillingFilePath, "billing"}, {project.BillingTSFilePath, "billing"},
	{project.APIKeysFilePath, "apikeys"}, {project.APIKeysTSFilePath, "apikeys"},
	{project.FlagsFilePath, "flags"}, {project.FlagsTSFilePath, "flags"},
	{project.RolesFilePath, "auth"}, {mcp.GeneratedPath, "mcp"},
}

func regeneratedPath(path string, packs []string) bool {
	return slices.ContainsFunc(generatedFiles, func(file struct{ path, pack string }) bool {
		return file.path == path && slices.Contains(packs, file.pack)
	})
}

// applyGeneratedPlan applies the packs, then replaces their defaults with generator output.
// The decisions compare the final output with the files before either write.
func applyGeneratedPlan(ctx context.Context, root string, lock *manifest.Lock, plan *Plan, force bool) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open generator inputs: %w\nTry: restore the upgrade worktree", err)
	}
	defer dir.Close()
	original := make(map[string]planDisk, len(generatedFiles))
	for _, file := range generatedFiles {
		if lock.Files[file.path].Class != manifest.Managed || !slices.Contains(lock.Packs, file.pack) {
			continue
		}
		disk, err := readPlanDisk(dir, file.path)
		if err != nil {
			return err
		}
		if disk.exists {
			// Preserve the owner's exact bytes, including line endings, on a conflict.
			disk.data, err = dir.ReadFile(file.path)
			if err != nil {
				return fmt.Errorf("read generator output %q: %w\nTry: check the file permissions", file.path, err)
			}
		}
		original[file.path] = disk
	}
	if err := writePlan(root, plan); err != nil {
		return err
	}
	return regenerate(ctx, root, lock, plan, original, force)
}

// regenerate also runs before the lock commit, after codemods and conflict resolution.
// A nil original checks current files before the lock commit.
func regenerate(ctx context.Context, root string, lock *manifest.Lock, plan *Plan, original map[string]planDisk, force bool) error {
	configConflict, mcpConflict := false, false
	for _, file := range plan.Files {
		if file.Action == ActionConflict {
			configConflict = configConflict || file.Path == project.ConfigFile
			mcpConflict = mcpConflict || strings.HasPrefix(file.Path, "api/internal/mcp/") && file.Path != mcp.GeneratedPath
		}
	}
	if configConflict {
		return nil // Resume generation after the configuration conflict is resolved.
	}
	for _, g := range []struct {
		name, pack string
		run        func(context.Context, gen.Input) (*gen.Output, error)
	}{
		{"billing", "billing", billing.Plan},
		{"apikeys", "apikeys", apikeys.Plan},
		{"flags", "flags", flags.Plan},
		{"roles", "auth", roles.Plan},
		{"mcp", "mcp", func(_ context.Context, in gen.Input) (*gen.Output, error) {
			file, err := mcp.RenderGenerated(in.Root, nil)
			return &gen.Output{Plan: vfs.Plan{Files: []vfs.File{file}}}, err
		}},
	} {
		if !slices.Contains(lock.Packs, g.pack) || g.name == "mcp" && mcpConflict {
			continue
		}
		managed := slices.ContainsFunc(generatedFiles, func(file struct{ path, pack string }) bool {
			return file.pack == g.pack && lock.Files[file.path].Class == manifest.Managed
		})
		if !managed {
			continue
		}
		if err := planContext(ctx); err != nil {
			return err
		}
		output, err := g.run(ctx, gen.Input{Root: root, Lock: lock})
		if err != nil {
			command := generatorCommand(g.name)
			return fmt.Errorf("upgrade generator %s failed: %w\nTry: correct the inputs and run %s", g.name, err, command)
		}
		var generated vfs.Plan
		for _, file := range output.Plan.Files {
			entry, tracked := lock.Files[file.Path]
			if !tracked || entry.Class != manifest.Managed || slices.Contains(lock.Tombstones, file.Path) {
				continue
			}
			var decision *FileDecision
			for i := range plan.Files {
				if plan.Files[i].Path == file.Path {
					decision = &plan.Files[i]
					break
				}
			}
			if decision == nil {
				continue
			}
			disk, ok := original[file.Path]
			if !ok {
				dir, err := os.OpenRoot(root)
				if err != nil {
					return fmt.Errorf("open generator output: %w\nTry: restore the upgrade worktree", err)
				}
				disk, err = readPlanDisk(dir, file.Path)
				_ = dir.Close()
				if err != nil {
					return err
				}
			}
			matchesOutput := bytes.Equal(manifest.Normalize(disk.data), manifest.Normalize(file.Data))
			// A resolved owner conflict can retain custom content. Never regenerate it again.
			if strings.HasPrefix(decision.Reason, generatedConflictReason) && !matchesOutput {
				continue
			}
			if !force && disk.exists && !matchesOutput && manifest.Hash(disk.data) != entry.Hash && !bytes.Equal(manifest.Normalize(disk.data), manifest.Normalize(decision.Data)) {
				decision.Action, decision.Data, decision.Mode = ActionConflict, disk.data, disk.mode
				decision.Reason = generatedConflictReason + ". Try: " + generatorCommand(g.name) + " in the worktree, or restart with spinup upgrade --force-managed"
				if original != nil {
					if err := generated.Add(vfs.File{Path: file.Path, Data: disk.data, Mode: disk.mode}); err != nil {
						return err
					}
				}
				continue
			}
			file.Mode = 0644
			if err := generated.Add(file); err != nil {
				return err
			}
			// Record even unchanged output, including a stale hash from a manual generation.
			entry.Hash, entry.Mode = manifest.Hash(file.Data), "0644"
			if err := lock.SetFile(file.Path, entry); err != nil {
				return err
			}
			if original == nil {
				continue
			}
			decision.Action, decision.Data, decision.Mode, decision.Reason = ActionOverwrite, file.Data, file.Mode, ""
			if !disk.exists {
				decision.Action = ActionCreate
			} else if matchesOutput && disk.mode == file.Mode {
				decision.Action = ActionUnchanged
			}
		}
		if _, err := vfs.Apply(root, generated, vfs.ApplyOptions{}); err != nil {
			return err
		}
	}
	return nil
}

const generatedEjectedReason = "ejected: generator skipped"
const generatedConflictReason = "owner edited generated file"

func generatorCommand(name string) string {
	if name == "auth" {
		name = "roles"
	}
	command := "spinup g " + name
	if name == "mcp" {
		command += " tool <name>"
	}
	return command
}
