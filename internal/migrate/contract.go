package migrate

import (
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// Contract file from the M2 pre-seed (ADR 0006). Main loop only: units do not edit it.

// Stage says when a migration runs during an upgrade step.
type Stage int

const (
	// Before runs before the OLD and NEW renders. It may change answers or move files,
	// so that the OLD render stays valid.
	Before Stage = iota + 1
	// After runs after the new files and dependencies are in place.
	After
)

// Ctx is the input of one migration.
type Ctx struct {
	// Root is the upgrade worktree. Migrations read and write files only under Root.
	Root string
	// From and To are the template versions of the current upgrade step.
	From, To string
	// Answers are the render answers. Before migrations may change them.
	Answers map[string]any
	// Lock is the lock of the step. Migrations do not write it; the runner records IDs.
	Lock *manifest.Lock
	// Runner runs every subprocess (go, gofmt, and similar).
	Runner exec.Runner
	// Old and New are the template versions of the step (packs and stack versions), so a
	// codemod can compare pins, such as a SvelteKit major bump. Old may be nil when the
	// old version cannot be loaded.
	Old, New *templates.Version
}

// Change is one planned file change, for `upgrade --plan`.
type Change struct {
	Path, Desc string
}

// Migration is one codemod. It runs when From < Since <= To and its ID is not in
// Lock.AppliedMigrations. Plan is pure. Apply is idempotent: running it twice gives
// the same tree as running it once.
type Migration struct {
	ID, Since, Desc string
	Stage           Stage
	Plan            func(*Ctx) ([]Change, error)
	Apply           func(*Ctx) error
}
