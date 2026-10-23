package provision

import (
	"context"
	"io"
)

// rotation is what rotateHostKey needs from the provisioning run (ADR 0017 §6).
// Unit U5b replaces this stub. U5 owns the callers.
type rotation struct {
	// Run runs argv on the VM as the deploy user and returns its stdout.
	Run func(ctx context.Context, argv ...string) (string, error)
	// Pin replaces host_keys in spinup.toml and in the SSH settings of the next connection.
	Pin func(keys []string) error
	// Reconnect closes the SSH control master and checks a new connection with the current pins.
	Reconnect func(ctx context.Context) error
	// Keys are the host keys that are pinned now.
	Keys []string
	// Out receives progress lines.
	Out io.Writer
}

// rotateHostKey replaces the host key that cloud-init got through the user data, in two phases.
// It returns the pinned keys after the run. A second run after a crash continues safely.
func rotateHostKey(ctx context.Context, r rotation) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return r.Keys, err
	}
	return r.Keys, nil
}

// The callers arrive with unit U5. Remove this line then.
var _, _ = rotateHostKey, addRotationTooling
