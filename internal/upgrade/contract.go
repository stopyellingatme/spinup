package upgrade

import (
	"context"
	"io"
	"io/fs"
	"sync"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// Contract file from the M2 pre-seed (ADR 0006). Main loop only: units do not edit it.

// Action is the planned outcome for one file.
type Action string

// The planned outcomes, with the decision rules from architecture §9 and critic resolutions 2 and 4.
const (
	// ActionUnchanged: the NEW render equals the file on disk. Nothing is written.
	ActionUnchanged Action = "unchanged"
	// ActionOverwrite: the disk hash equals the lock hash, or --force-managed applies. Write NEW.
	ActionOverwrite Action = "overwrite"
	// ActionMerge: a clean three-way merge. Write the merge result.
	ActionMerge Action = "merge"
	// ActionConflict: a three-way merge with conflict markers. Write the result with markers.
	ActionConflict Action = "conflict"
	// ActionCreate: the file is new in the template, and the path is free. Write NEW.
	ActionCreate Action = "create"
	// ActionDelete: the template removed the file, and the disk hash equals the lock hash.
	ActionDelete Action = "delete"
	// ActionRename: the template moved the file (renamed_from). Write at Path, remove From.
	ActionRename Action = "rename"
	// ActionOrphan: the template removed the file, but the user changed it. Keep it, record an orphan.
	ActionOrphan Action = "orphan"
	// ActionKeep: leave the disk alone (seed, tombstone, or a changed binary or large file).
	ActionKeep Action = "keep"
)

// FileDecision is the plan for one project path.
type FileDecision struct {
	// Path is the target path (slash-separated, relative to the project root).
	Path string
	// From is the old path of a rename. It is empty otherwise.
	From   string
	Action Action
	// Class and Template come from the NEW render (or the lock for delete, orphan, and keep).
	Class    manifest.Class
	Template string
	// Data is the content to write for overwrite, merge, conflict, create, and rename.
	Data []byte
	// Mode is the file mode to apply. Modes are compared and applied apart from content.
	Mode fs.FileMode
	// Reason is one short line for the plan output.
	Reason string
}

// Plan is the decision for every tracked or rendered path of one upgrade step.
type Plan struct {
	From, To string
	// Files is sorted by Path.
	Files []FileDecision
	// Tombstoned lists tracked files missing from disk that become tombstones (resolution 4).
	Tombstoned []string
	// MissingManaged lists managed files missing from disk (check warns about them).
	MissingManaged []string
}

// Options are the user's choices for `spinup upgrade`.
type Options struct {
	To                                      string
	AllowDirty, ForceManaged, NoVerify, Yes bool
	CommitConflicts, Offline, DryRun        bool
}

// Step is one upgrade step (one template version to the next) inside the upgrade worktree.
type Step struct {
	From, To *templates.Version
	// Worktree is the root of the upgrade worktree. Every phase writes only there.
	Worktree string
	// Lock is the lock being built for this step. Phases may update it (for example
	// AppliedMigrations). The state machine writes it after the After phase.
	Lock *manifest.Lock
	// Answers are the render answers. The Before phase may change them.
	Answers map[string]any
	// Plan is set after planning. It is nil in the Before phase.
	Plan    *Plan
	Options Options
	Runner  exec.Runner
	Out     io.Writer
}

// Phases are the pluggable parts of a step. The state machine (unit M2.4) calls them in
// this order: Before, plan, write the plan's files, Apply, Deps, commit, GoFix (its own
// commit when it changed files), After, lock write, commit, Verify. The state machine
// itself writes the plan's files; Apply is extra work after that write (for example the
// kit replace directive). A nil phase is skipped. Unit M2.8 sets them.
type Phases struct {
	Before func(ctx context.Context, s *Step) error
	Apply  func(ctx context.Context, s *Step) error
	Deps   func(ctx context.Context, s *Step) error
	GoFix  func(ctx context.Context, s *Step) (changed bool, err error)
	After  func(ctx context.Context, s *Step) error
	Verify func(ctx context.Context, s *Step) error
}

// Extension adds flags and work to `spinup upgrade`. Unit M4.8 adds --ui with it.
// Run is called once per step, after the After phase.
type Extension struct {
	Name  string
	Flags func(fs *pflag.FlagSet)
	Run   func(ctx context.Context, s *Step) error
}

var (
	hooksMu    sync.RWMutex
	phases     Phases
	extensions []Extension
)

// SetPhases installs the step phases. Call it from init.
func SetPhases(p Phases) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	phases = p
}

// CurrentPhases returns the installed step phases.
func CurrentPhases() Phases {
	hooksMu.RLock()
	defer hooksMu.RUnlock()
	return phases
}

// RegisterExtension adds an upgrade extension. Call it from init.
func RegisterExtension(e Extension) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	extensions = append(extensions, e)
}

// Extensions returns the registered extensions in registration order.
func Extensions() []Extension {
	hooksMu.RLock()
	defer hooksMu.RUnlock()
	return append([]Extension(nil), extensions...)
}
