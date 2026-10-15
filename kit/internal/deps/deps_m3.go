//go:build deps

package deps

// M3 pre-seed.
import (
	_ "github.com/riverqueue/river"
	_ "github.com/riverqueue/river/riverdriver/riverpgxv5"
	_ "github.com/riverqueue/river/rivermigrate"
)
