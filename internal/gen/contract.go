package gen

import (
	"context"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Contract file from the M3 pre-seed (ADR 0007). Main loop only: units do not edit it.

// PostStep is a tool run after a generator's files are written (ADR 0007 §2).
type PostStep string

// The post-steps, run in the order openapi, sqlc, client, service-clients, gofmt when requested.
const (
	PostOpenAPI PostStep = "openapi"
	PostSQLC    PostStep = "sqlc"
	PostGofmt   PostStep = "gofmt"
	// PostClient regenerates web/src/lib/api/schema.d.ts from the OpenAPI bundle (M4, ADR 0008).
	PostClient PostStep = "client"
	// PostServiceClients regenerates the Go clients of the sidecar services (ADR 0022).
	PostServiceClients PostStep = "service-clients"
)

// Input is what a generator reads. Generators never write to disk.
type Input struct {
	// Root is the project root (the directory that holds spinup.toml).
	Root string
	// Lock is the project lock. Generators read it (answers, tracked files) and never change it.
	Lock *manifest.Lock
	// Args are the positional arguments after the generator name.
	Args []string
	// Flags holds the generator's own flags, parsed.
	Flags *pflag.FlagSet
}

// Output is what a generator returns.
type Output struct {
	// Plan holds the files to write. Each file has a Class.
	Plan vfs.Plan
	// PostSteps are run after the write, in the order of the constants above.
	PostSteps []PostStep
	// Next holds up to two commands to suggest after the run.
	Next []string
	// Commands are external generator commands (such as `pnpm dlx shadcn-svelte@<v> add
	// button`) that the runner runs through exec.Runner after the write and before the
	// post-steps. Each is an argv; Dir is relative to the project root. --dry-run prints them.
	Commands []Command
	// Edits lists the plan paths that the generator built from the current content on disk
	// (an in-place edit that keeps the user's content, such as a rule appended to a seed
	// file). The runner updates them without --force and keeps their lock template.
	Edits []string
}

// Command is one external command a generator asks the runner to run (ADR 0008).
type Command struct {
	Dir  string
	Argv []string
}

// Generator is one `spinup generate <name>` kind.
type Generator struct {
	// Name is the kind, such as "resource". Aliases are other names for it.
	Name    string
	Aliases []string
	// Short is one help line. Usage is the argument synopsis, such as "<name> <field:type>...".
	Short, Usage string
	// Flags adds the generator's own flags.
	Flags func(fs *pflag.FlagSet)
	// Run plans the files. It must be deterministic for the same input.
	Run func(ctx context.Context, in Input) (*Output, error)
}
