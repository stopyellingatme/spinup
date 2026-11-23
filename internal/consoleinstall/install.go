// Package consoleinstall installs the console and collects facts over SSH.
package consoleinstall

import (
	"context"
	"errors"

	"github.com/stopyellingatme/spinup/internal/remote"
)

// Spec identifies the installed console and its feature selection.
type Spec struct {
	App, Env, Profile, Backend, Origin, Image string
	Jobs                                      bool
}

// Install installs the console files and starts the web service.
func Install(_ context.Context, _ remote.Runner, _ Spec, _ []byte) error {
	return errors.New("cannot install console: the installer is unavailable\nTry: upgrade the CLI to a version with the console installer")
}

// ReconcileQuery starts or removes the relay after monitoring reconciliation.
func ReconcileQuery(_ context.Context, _ remote.Runner, _ Spec, _ []byte) error {
	return errors.New("cannot reconcile console queries: the installer is unavailable\nTry: upgrade the CLI to a version with the console installer")
}

// Refresh requests a host sample when the previous sample is old or missing.
func Refresh(_ context.Context, _ remote.Runner, _ string) error {
	return errors.New("cannot refresh console facts: the collector is unavailable\nTry: upgrade the CLI to a version with the console collector")
}
