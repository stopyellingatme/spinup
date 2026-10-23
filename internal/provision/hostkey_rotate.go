package provision

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
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

// hostKeyTool is the script that the user data installs on the VM.
const hostKeyTool = "/usr/local/sbin/spinup-host-key"

const rotateTry = "Try: run spinup deploy digital-ocean again; the pinned keys stay valid."

// rotateHostKey replaces the host key that cloud-init got through the user data, in two phases.
// It returns the pinned keys after the run. A second run after a crash continues safely.
func rotateHostKey(ctx context.Context, r rotation) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return r.Keys, err
	}
	tool := func(cmd string) (string, error) {
		return r.Run(ctx, "sudo", "-n", hostKeyTool, cmd)
	}

	status, err := tool("status")
	if err != nil {
		if toolMissing(status, err) {
			rotateSay(r, "warning: the VM has no %s. The host key stays as it is.\n", hostKeyTool)
			return r.Keys, nil
		}
		return r.Keys, fmt.Errorf("host key rotation: cannot read the rotation status on the VM: %w\n%s", err, rotateTry)
	}
	status = strings.TrimSpace(status)

	if status == "rotated" {
		if len(r.Keys) == 1 {
			return r.Keys, nil
		}
		return rotateConverge(ctx, r, tool)
	}
	if status != "pending" {
		return r.Keys, fmt.Errorf("host key rotation: the VM reported the unknown status %q\n%s", status, rotateTry)
	}

	// Phase 1: the VM creates the new key. Both keys stay valid in the pin.
	out, err := tool("next")
	if err != nil {
		return r.Keys, fmt.Errorf("host key rotation: the VM could not create a new host key: %w\n%s", err, rotateTry)
	}
	next, err := parseHostKey(out)
	if err != nil {
		return r.Keys, fmt.Errorf("host key rotation: the new host key from the VM is not valid: %w\n%s", err, rotateTry)
	}
	both := dedupeKeys(append(append([]string{}, r.Keys...), next))
	if err := r.Pin(both); err != nil {
		return r.Keys, fmt.Errorf("host key rotation: cannot pin the new host key: %w\n%s", err, rotateTry)
	}

	// Phase 2: the VM serves the new key.
	out, err = tool("activate")
	if err != nil {
		return both, fmt.Errorf("host key rotation: the VM could not activate the new host key: %w\n%s", err, rotateTry)
	}
	cur, err := parseHostKey(out)
	if err != nil {
		return both, fmt.Errorf("host key rotation: the active host key from the VM is not valid: %w\n%s", err, rotateTry)
	}
	if cur != next {
		return both, fmt.Errorf("host key rotation: the VM did not take the new host key\n%s", rotateTry)
	}
	if err := r.Reconnect(ctx); err != nil {
		return both, fmt.Errorf("host key rotation: a new connection does not verify against the new host key: %w\n%s", err, rotateTry)
	}
	if err := r.Pin([]string{next}); err != nil {
		return both, fmt.Errorf("host key rotation: cannot keep only the new host key: %w\n%s", err, rotateTry)
	}
	rotateSay(r, "Host key rotated.\n")
	return []string{next}, nil
}

// rotateConverge finishes a rotation that stopped after the VM took the new key.
func rotateConverge(ctx context.Context, r rotation, tool func(string) (string, error)) ([]string, error) {
	// With no new key waiting, activate only prints the current key.
	out, err := tool("activate")
	if err != nil {
		return r.Keys, fmt.Errorf("host key rotation: cannot read the current host key on the VM: %w\n%s", err, rotateTry)
	}
	cur, err := parseHostKey(out)
	if err != nil {
		return r.Keys, fmt.Errorf("host key rotation: the current host key from the VM is not valid: %w\n%s", err, rotateTry)
	}
	known := false
	for _, k := range r.Keys {
		if k == cur {
			known = true
		}
	}
	if !known {
		return r.Keys, fmt.Errorf("host key rotation: the VM serves a host key that is not pinned\n%s", rotateTry)
	}
	if err := r.Reconnect(ctx); err != nil {
		return r.Keys, fmt.Errorf("host key rotation: a new connection does not verify: %w\n%s", err, rotateTry)
	}
	if err := r.Pin([]string{cur}); err != nil {
		return r.Keys, fmt.Errorf("host key rotation: cannot keep only the current host key: %w\n%s", err, rotateTry)
	}
	return []string{cur}, nil
}

// toolMissing reports whether the VM lacks the script or the sudo rule for it.
func toolMissing(out string, err error) bool {
	text := out + " " + err.Error()
	for _, s := range []string{"command not found", "No such file", "a password is required", "not allowed to execute"} {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// parseHostKey checks that out holds one ssh-ed25519 line and returns it without the comment.
func parseHostKey(out string) (string, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		return "", errors.New("expected one line")
	}
	f := strings.Fields(lines[0])
	if len(f) < 2 || f[0] != "ssh-ed25519" {
		return "", errors.New("expected an ssh-ed25519 key")
	}
	for _, c := range f[1] {
		ok := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '='
		if !ok {
			return "", errors.New("the key is not base64")
		}
	}
	return f[0] + " " + f[1], nil
}

func dedupeKeys(keys []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func rotateSay(r rotation, format string, a ...any) {
	if r.Out != nil {
		_, _ = fmt.Fprintf(r.Out, format, a...)
	}
}
