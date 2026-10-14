//go:build deps

// Package deps pins the modules that a milestone needs before any code imports them,
// so that `go mod tidy` keeps them. Main loop only (see CLAUDE.md).
package deps

// M0 pre-seed.
import (
	_ "github.com/pelletier/go-toml/v2"
	_ "github.com/rogpeppe/go-internal/testscript"
	_ "github.com/spf13/cobra"
	_ "go.yaml.in/yaml/v3"
	_ "golang.org/x/term"
)
