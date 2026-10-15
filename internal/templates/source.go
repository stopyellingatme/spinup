package templates

import (
	"context"
	"errors"
	"io/fs"

	"github.com/stopyellingatme/spinup/internal/stackversions"
)

// Contract file from the M2 pre-seed (ADR 0006). Main loop only: units do not edit it.

// Version is one template version: the packs and the stack versions that render it.
// A render is a pure function of the version, the answers, the plugin set, and Stack.
type Version struct {
	// Version is the semantic version without a "v" prefix, such as "0.2.0" or "0.0.0-dev".
	Version string
	// Packs is rooted at the packs directory, like Embedded.
	Packs fs.FS
	// Stack holds the stack versions that shipped with these packs.
	Stack *stackversions.Stack
	// KitMigration is the kit/MIGRATION.json that shipped with this version. It may be nil.
	KitMigration []byte
}

// Source loads template versions. Package internal/templates/store implements it.
type Source interface {
	// Versions lists every version the source can load, in ascending semantic-version order.
	Versions(ctx context.Context) ([]string, error)
	// Load returns one version. It wraps ErrVersionUnavailable when the version is not
	// embedded, not cached, and cannot be downloaded (for example with --offline).
	Load(ctx context.Context, version string) (*Version, error)
}

// ErrVersionUnavailable reports that a template version cannot be loaded.
var ErrVersionUnavailable = errors.New("template version unavailable")
