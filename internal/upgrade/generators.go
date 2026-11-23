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
func applyGeneratedPlan(ctx context.Context, root string, lock *manifest.Lock, plan *Plan) error {
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
		original[file.path] = disk
	}
	if err := writePlan(root, plan); err != nil {
		return err
	}
	return regenerate(ctx, root, lock, plan, original)
}

// regenerate also runs before the lock commit, after codemods and conflict resolution.
// A nil original records the output without changing the previously reported decisions.
func regenerate(ctx context.Context, root string, lock *manifest.Lock, plan *Plan, original map[string]planDisk) error {
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
			command := "spinup g " + g.name
			if g.name == "mcp" {
				command += " tool <name>"
			}
			return fmt.Errorf("upgrade generator %s failed: %w\nTry: correct the inputs and run %s", g.name, err, command)
		}
		var generated vfs.Plan
		for _, file := range output.Plan.Files {
			entry, tracked := lock.Files[file.Path]
			if !tracked || entry.Class != manifest.Managed || slices.Contains(lock.Tombstones, file.Path) {
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
			for i := range plan.Files {
				d := &plan.Files[i]
				if d.Path != file.Path {
					continue
				}
				d.Action, d.Data, d.Mode, d.Reason = ActionOverwrite, file.Data, file.Mode, ""
				disk := original[file.Path]
				if !disk.exists {
					d.Action = ActionCreate
				} else if bytes.Equal(disk.data, manifest.Normalize(file.Data)) && disk.mode == file.Mode {
					d.Action = ActionUnchanged
				}
			}
		}
		if _, err := vfs.Apply(root, generated, vfs.ApplyOptions{}); err != nil {
			return err
		}
	}
	return nil
}
