package merge

import "context"

// Contract file from the M2 pre-seed (ADR 0006). Main loop only: units do not edit it.

// MaxSize is the largest file that an upgrade merges. The planner overwrites or keeps a
// larger file, and it never sends one to a Merger.
const MaxSize = 1 << 20

// Input is one three-way merge. The caller normalizes all three inputs (LF line endings,
// one trailing newline) and never passes binary data.
type Input struct {
	// Path is the slash-separated project path, used in labels and messages.
	Path string
	// Base is the OLD render. It is empty when the file is new in the template.
	Base []byte
	// Ours is the file on disk (the user's version).
	Ours []byte
	// Theirs is the NEW render.
	Theirs []byte
}

// Result is a merged file.
type Result struct {
	// Data is the merged content. It holds diff3 conflict markers when Conflicted is true.
	Data []byte
	// Conflicted is true when at least one hunk could not be merged.
	Conflicted bool
}

// Merger merges text three ways.
type Merger interface {
	Merge(ctx context.Context, in Input) (Result, error)
}
