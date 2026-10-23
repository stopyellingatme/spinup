package provision

import (
	"context"
	"os"
	"path/filepath"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

// newHostKey makes an ed25519 host key and writes the public half to host_keys.
// It does this before the create request, so the first connection is pinned (ADR 0017 §6).
// The private key stays in memory until the user data is built.
func (r *run) newHostKey(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "spinup-hostkey-*")
	if err != nil {
		return failWrap("make a temporary directory for the host key", err, "check the temporary directory permissions")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0o700); err != nil {
		return failWrap("protect the temporary directory", err, "check the temporary directory permissions")
	}
	path := filepath.Join(dir, "k")
	if _, err := r.d.Exec.Run(ctx, runexec.Cmd{Name: "ssh-keygen", Args: []string{"-q", "-t", "ed25519", "-N", "", "-C", "", "-f", path}}); err != nil {
		return failWrap("generate the host key", err, "install ssh-keygen, or run spinup doctor")
	}
	priv, err := os.ReadFile(path)
	if err != nil {
		return failWrap("read the private host key", err, "run the command again")
	}
	pubLine, err := os.ReadFile(path + ".pub")
	if err != nil {
		return failWrap("read the public host key", err, "run the command again")
	}
	pub, err := normalizeKey(string(pubLine))
	if err != nil {
		return failWrap("read the public host key", err, "run the command again")
	}
	if err := r.pinHostKeys([]string{pub}); err != nil {
		return err
	}
	r.hostPub, r.hostPriv = pub, string(priv)
	return nil
}
