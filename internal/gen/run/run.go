package run

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/pflag"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Options controls preview, output, overwrites, and tool execution.
type Options struct {
	// Root is the project directory supplied by the caller.
	Root string
	// DryRun lists file actions without writing or running tools.
	DryRun bool
	// Diff prints unified diffs without writing or running tools.
	Diff bool
	// Force permits changes to existing files with different content.
	Force bool
	// JSON selects structured output for applied plans and previews.
	JSON bool
	// Out receives output. A nil writer discards output.
	Out io.Writer
	// Runner executes post-steps. A nil runner uses exec.OSRunner.
	Runner exec.Runner
}

// FileResult describes a planned file and its action.
type FileResult struct {
	// Path is relative to the project root.
	Path string `json:"path"`
	// Action is create, update, or unchanged.
	Action string `json:"action"`
	// Class controls how upgrades treat the file.
	Class manifest.Class `json:"class"`
}

// Result describes the files, ordered post-steps, and suggested commands.
type Result struct {
	// Files lists every planned file in path order.
	Files []FileResult `json:"files"`
	// PostSteps lists each requested tool once, in execution order.
	PostSteps []gen.PostStep `json:"post_steps"`
	// Next contains the generator's suggested commands.
	Next []string `json:"next"`
	// Commands lists the external commands, run after the write and before the post-steps.
	Commands []gen.Command `json:"commands,omitempty"`
}

// Run validates and previews or applies g's plan in the supplied project root.
func Run(ctx context.Context, g gen.Generator, args []string, flags *pflag.FlagSet, o Options) (*Result, error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Runner == nil {
		o.Runner = exec.OSRunner{}
	}
	lockPath := filepath.Join(o.Root, "spinup.lock")
	lock, err := manifest.Load(lockPath)
	if err != nil {
		return nil, err
	}
	if g.Run == nil {
		return nil, fmt.Errorf("cannot run generator %q: Run is nil\nTry: choose a registered generator", g.Name)
	}
	output, err := g.Run(ctx, gen.Input{Root: o.Root, Lock: lock, Args: args, Flags: flags})
	if err != nil {
		return nil, withHint(fmt.Sprintf("generator %q failed", g.Name), err, "spinup g "+g.Name+" --help")
	}
	if output == nil {
		return nil, fmt.Errorf("generator %q returned no plan\nTry: repair the generator to return gen.Output", g.Name)
	}
	plan, err := validatePlan(output.Plan)
	if err != nil {
		return nil, err
	}
	commands, err := validateCommands(g.Name, output.Commands)
	if err != nil {
		return nil, err
	}
	changes, err := vfs.Diff(o.Root, plan)
	if err != nil {
		return nil, withHint("compare generator files", err, "check the project paths and permissions")
	}
	result := &Result{Files: make([]FileResult, 0, len(changes)), PostSteps: orderedSteps(output.PostSteps), Next: append([]string{}, output.Next...), Commands: commands}
	var writes vfs.Plan
	var conflicts []string
	for i, change := range changes {
		file := plan.Files[i]
		// Identical content is a no-op, including a permissions-only difference.
		action := change.Kind.String()
		if change.Kind == vfs.ModeOnly {
			action = "unchanged"
		}
		result.Files = append(result.Files, FileResult{Path: file.Path, Action: action, Class: manifest.Class(file.Class)})
		if action == "unchanged" {
			continue
		}
		if change.Kind == vfs.Update && !o.Force && !generatorOwned(o.Root, file, lock) && !slices.Contains(output.Edits, file.Path) {
			conflicts = append(conflicts, file.Path)
		}
		writes.Files = append(writes.Files, file)
	}
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("generator %q would overwrite existing files with different content:\n  %s\nTry: pass --force, or remove the files", g.Name, strings.Join(conflicts, "\n  "))
	}
	if o.DryRun || o.Diff {
		if o.JSON {
			return result, printResult(o.Out, result)
		}
		for i, file := range result.Files {
			if o.Diff {
				_, err = fmt.Fprint(o.Out, changes[i].UnifiedDiff)
			} else {
				_, err = fmt.Fprintf(o.Out, "%s %s (%s)\n", file.Action, file.Path, file.Class)
			}
			if err != nil {
				return result, withHint("write generator preview", err, "check the output destination")
			}
		}
		return result, previewCommands(o.Out, result.Commands)
	}
	if len(writes.Files) > 0 {
		if _, err := vfs.Apply(o.Root, writes, vfs.ApplyOptions{}); err != nil {
			return result, err
		}
		for _, file := range writes.Files {
			mode := file.Mode.Perm()
			if mode == 0 {
				mode = 0644
			}
			template := "gen/" + g.Name
			if entry, ok := lock.Files[file.Path]; ok && slices.Contains(output.Edits, file.Path) {
				template = entry.Template
			}
			if err := lock.SetFile(file.Path, manifest.FileEntry{Class: manifest.Class(file.Class), Template: template, Hash: manifest.Hash(file.Data), Mode: fmt.Sprintf("%04o", mode)}); err != nil {
				return result, err
			}
		}
		if err := lock.Save(lockPath); err != nil {
			return result, err
		}
	}
	commandOut := o.Out
	if o.JSON {
		commandOut = os.Stderr
	}
	if err := runCommands(ctx, o.Root, result.Commands, o.Runner, commandOut); err != nil {
		return result, err
	}
	runners, err := stepRunners(result.PostSteps)
	if err != nil {
		return result, err
	}
	for i, step := range result.PostSteps {
		if err := runners[i](ctx, o.Root, plan.Paths(), o.Runner); err != nil {
			return result, fmt.Errorf("generator post-step %q failed: %w\nTry: %s", step, err, retryStep(step, plan.Paths()))
		}
	}
	if o.JSON {
		return result, printResult(o.Out, result)
	}
	created, updated := 0, 0
	for _, file := range result.Files {
		switch file.Action {
		case "create":
			created++
		case "update":
			updated++
		}
	}
	if _, err := fmt.Fprintf(o.Out, "Created %d, updated %d files\n", created, updated); err != nil {
		return result, withHint("write generator summary", err, "check the output destination")
	}
	for _, next := range result.Next {
		if _, err := fmt.Fprintf(o.Out, "Next: %s\n", next); err != nil {
			return result, withHint("write generator next command", err, "check the output destination")
		}
	}
	return result, nil
}

func validatePlan(plan vfs.Plan) (vfs.Plan, error) {
	var validated vfs.Plan
	for _, file := range plan.Files {
		clean, err := vfs.CleanPath(file.Path)
		if err != nil {
			return vfs.Plan{}, err
		}
		if clean != file.Path || clean == "spinup.lock" || file.Delete || !manifest.Class(file.Class).Valid() {
			return vfs.Plan{}, fmt.Errorf("invalid generator file %q: require a clean path, a valid class, and file content\nTry: repair the generator plan", file.Path)
		}
		if err := validated.Add(file); err != nil {
			return vfs.Plan{}, err
		}
	}
	return validated.Sorted(), nil
}

func printResult(out io.Writer, result *Result) error {
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return withHint("write generator JSON", err, "check the output destination")
	}
	return nil
}

func withHint(what string, err error, hint string) error {
	if strings.Contains(err.Error(), "Try:") {
		return fmt.Errorf("%s: %w", what, err)
	}
	return fmt.Errorf("%s: %w\nTry: %s", what, err, hint)
}

// generatorOwned reports whether a generator may update an existing file without --force:
// the plan marks it managed, and either a generator wrote it last (lock template "gen/...")
// or it is a managed pack file that the user never edited (disk hash equals lock hash),
// such as routes_gen.go on a fresh app (ADR 0007 §1).
func generatorOwned(root string, file vfs.File, lock *manifest.Lock) bool {
	entry, ok := lock.Files[file.Path]
	if file.Class != string(manifest.Managed) || !ok || entry.Class != manifest.Managed {
		return false
	}
	if strings.HasPrefix(entry.Template, "gen/") {
		return true
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.Path)))
	return err == nil && manifest.Hash(data) == entry.Hash
}
