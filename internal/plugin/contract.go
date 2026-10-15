// Package plugin holds the contract between plugin resolution and the code that renders plugin
// packs (ADR 0013 §4).
//
// Contract file from the M9 pre-seed. Main loop only: units do not edit it.
package plugin

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sync"
)

// API is the plugin API version that this CLI supports.
const API = 1

// AllowlistFile is the committed allowlist, relative to the project root.
const AllowlistFile = ".spinup/plugins.allow"

// Meta is the decoded plugin.toml of a plugin.
type Meta struct {
	Name        string          `toml:"name"`
	Version     string          `toml:"version"`
	API         int             `toml:"api"`
	Spinup      string          `toml:"spinup"`
	Description string          `toml:"description"`
	Generators  []GeneratorSpec `toml:"generators"`
}

// GeneratorSpec is a declarative generator of a plugin.
type GeneratorSpec struct {
	Name  string    `toml:"name"`
	Short string    `toml:"short"`
	Usage string    `toml:"usage"`
	Args  []string  `toml:"args"`
	Files []GenFile `toml:"files"`
}

// GenFile is one file of a declarative generator. Path and Source are templates over the
// generator arguments. Class is managed, scaffold, or seed.
type GenFile struct {
	Path   string `toml:"path"`
	Source string `toml:"source"`
	Class  string `toml:"class"`
}

// Loaded is one resolved and verified plugin.
type Loaded struct {
	Meta Meta
	// Source is the declared source, such as "path:./plugins/foo" or "git:https://host/repo".
	Source string
	// SHA256 is the hex hash of the normalized plugin tree.
	SHA256 string
	// FS is rooted at the plugin directory (plugin.toml, pack.toml, pack.d/, files/).
	FS fs.FS
}

// Options selects the project whose plugins Resolve loads.
type Options struct {
	// Root is the project root (the directory of spinup.toml).
	Root string
	// Offline forbids fetching. A plugin missing from the cache is an error.
	Offline bool
	// Versions overrides the declared version of named plugins (plugin update, the OLD render).
	Versions map[string]string
	// Out receives progress lines. Nil discards them.
	Out io.Writer
}

// ErrNoResolver means that no package registered a resolver (a build without internal/plugin/pack).
var ErrNoResolver = errors.New("plugin support is not in this build")

// Resolver resolves the plugins of a project.
type Resolver func(ctx context.Context, o Options) ([]Loaded, error)

var (
	resolverMu sync.Mutex
	resolver   Resolver
)

// SetResolver installs the resolver. Unit M9.4 calls it from init() in internal/plugin/pack.
func SetResolver(r Resolver) {
	resolverMu.Lock()
	defer resolverMu.Unlock()
	resolver = r
}

// Resolve returns the plugins of the project in declaration order, each verified against the
// allowlist and spinup.lock. A project without plugins returns nil and no error.
func Resolve(ctx context.Context, o Options) ([]Loaded, error) {
	resolverMu.Lock()
	r := resolver
	resolverMu.Unlock()
	if r == nil {
		return nil, ErrNoResolver
	}
	return r(ctx, o)
}
