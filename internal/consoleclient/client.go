// Package consoleclient reads the console through authenticated SSH forwards.
package consoleclient

import (
	"context"
	"errors"
	"io"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

// Options configures console access and terminal output.
type Options struct {
	Dir, Env                          string
	JSON, NoBrowser, AcceptNewHostKey bool
	Out, ErrOut                       io.Writer
}

// Open opens the console and maintains its SSH forwards until cancellation.
func Open(_ context.Context, _ Options, _ runexec.Runner) error {
	return errors.New("cannot open console: the client is unavailable\nTry: upgrade the CLI to a version with the console client")
}

// Status writes a validated overview from the deployed console.
func Status(_ context.Context, _ Options, _ runexec.Runner) error {
	return errors.New("cannot read console: the client is unavailable\nTry: upgrade the CLI to a version with the console client")
}
