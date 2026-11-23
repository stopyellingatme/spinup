package remote

import (
	"context"
	"errors"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

// ForwardLoopback forwards a VM loopback port to an ephemeral local loopback port.
// The returned function closes only the forward owned by this invocation.
func ForwardLoopback(_ context.Context, _ Host, _ runexec.Runner, _ Options, _ int) (localPort int, closeForward func() error, err error) {
	return 0, nil, errors.New("cannot forward the loopback port: SSH forwarding is unavailable\nTry: upgrade the CLI to a version with loopback forwarding")
}
