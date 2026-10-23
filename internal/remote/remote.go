package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
)

// Host identifies a destination in the user's SSH configuration.
type Host struct {
	Name, Address, User string
	Port                int
	// HostKeys pins the host keys ("ssh-ed25519 AAAA...") of the VM. When it is set, SSH trusts
	// only these keys and ignores known_hosts and --accept-new-host-key (ADR 0017).
	HostKeys []string
}

// HostFromEnv validates the destination. A zero port leaves the port to the SSH config
// (which defaults to 22), so a Port line in ~/.ssh/config or SPINUP_SSH_CONFIG wins.
func HostFromEnv(env project.Env) (Host, error) {
	h := Host{Name: env.Name, Address: env.Host, User: env.User, Port: env.Port, HostKeys: env.HostKeys}
	return h, validateHost(h)
}

func validateHost(h Host) error {
	invalid := func(s string) bool {
		return strings.HasPrefix(s, "-") || strings.ContainsAny(s, "@\x00") || strings.IndexFunc(s, unicode.IsSpace) >= 0
	}
	if h.Address == "" || invalid(h.Address) || invalid(h.User) || h.Port < 0 || h.Port > 65535 {
		return fmt.Errorf("invalid SSH destination for environment %q: host, user, or port is invalid\nTry: set host, user, and port in spinup.toml", h.Name)
	}
	return nil
}

// Cmd describes a remote argv invocation, with optional streams and redaction.
type Cmd struct {
	Argv           []string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Timeout        time.Duration
	Redact         []string
}

// Runner runs remote commands and replaces remote files atomically.
type Runner interface {
	Run(context.Context, Cmd) (runexec.Result, error)
	Upload(context.Context, []byte, string, fs.FileMode) error
	Close(context.Context) error
}

// Options configures host key acceptance and the local cache location.
type Options struct {
	AcceptNewHostKey bool
	// CacheDir overrides os.UserCacheDir. The spinup/ssh directory is created below it.
	CacheDir string
}

type sshRunner struct {
	runner      runexec.Runner
	args        []string
	destination string
	err         error
}

// New creates a system SSH runner. Initialization errors are returned by its methods.
func New(h Host, r runexec.Runner, o Options) Runner {
	s := &sshRunner{runner: r, err: validateHost(h), destination: h.Address}
	if h.User != "" {
		s.destination = h.User + "@" + h.Address
	}
	if s.err != nil {
		return s
	}
	cache := o.CacheDir
	if cache == "" {
		cache, s.err = os.UserCacheDir()
		if s.err != nil {
			return s
		}
	}
	dir := filepath.Join(cache, "spinup", "ssh")
	// OpenSSH hashes the resolved host, port, and user through %C.
	// Escape percent signs in the cache directory before SSH expands its tokens.
	control := filepath.Join(strings.ReplaceAll(dir, "%", "%%"), "%C")
	// Reserve the temporary suffix that OpenSSH adds before it renames the socket.
	// sockaddr_un.sun_path allows 104 bytes on macOS, including the terminator.
	if len(dir)+1+40+17 >= 104 {
		s.err = fmt.Errorf("SSH control path exceeds the Unix socket limit\nTry: select a shorter user cache directory")
		return s
	}
	if s.err = os.MkdirAll(dir, 0700); s.err != nil {
		return s
	}
	if s.err = os.Chmod(dir, 0700); s.err != nil {
		return s
	}
	s.args = []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + control, "-o", "ControlPersist=60s"}
	if h.Port != 0 {
		s.args = append(s.args, "-p", strconv.Itoa(h.Port))
	}
	s.args = append(s.args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=10")
	if o.AcceptNewHostKey {
		s.args = append(s.args, "-o", "StrictHostKeyChecking=accept-new")
	}
	// SPINUP_SSH_CONFIG points ssh at another config file (ssh -F), for test VMs and CI.
	if config := os.Getenv("SPINUP_SSH_CONFIG"); config != "" {
		s.args = append([]string{"-F", config}, s.args...)
	}
	return s
}

// Quote quotes one argument for a remote POSIX shell, including empty arguments.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func (s *sshRunner) Run(ctx context.Context, c Cmd) (runexec.Result, error) {
	if len(c.Argv) == 0 {
		return runexec.Result{}, fmt.Errorf("remote command has no arguments\nTry: supply a command")
	}
	parts := make([]string, len(c.Argv))
	for i, arg := range c.Argv {
		if strings.ContainsRune(arg, 0) {
			return runexec.Result{}, fmt.Errorf("remote argument contains a NUL byte\nTry: remove the NUL byte")
		}
		parts[i] = Quote(arg)
	}
	return s.run(ctx, c, nil, strings.Join(parts, " "))
}

func (s *sshRunner) run(ctx context.Context, c Cmd, control []string, command string) (runexec.Result, error) {
	if s.err != nil {
		return runexec.Result{}, fmt.Errorf("initialize SSH connection: %w\nTry: inspect the SSH destination and cache directory", s.err)
	}
	args := append([]string(nil), s.args...)
	args = append(args, control...)
	args = append(args, "--", s.destination)
	if command != "" {
		args = append(args, command)
	}
	// The remote shell encoding can split a secret around embedded quotes.
	// Redact that representation before exec formats a command error.
	secrets := append([]string(nil), c.Redact...)
	for _, secret := range c.Redact {
		escaped := strings.ReplaceAll(secret, "'", "'\"'\"'")
		if escaped != secret {
			secrets = append(secrets, escaped)
		}
	}
	return s.runner.Run(ctx, runexec.Cmd{Name: "ssh", Args: args, Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr, Timeout: c.Timeout, Redact: secrets})
}

func (s *sshRunner) Upload(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	if path == "" || strings.ContainsRune(path, 0) {
		return fmt.Errorf("invalid upload path\nTry: supply a nonempty path without NUL bytes")
	}
	// mktemp reserves a file beside the destination. Rename stays on one filesystem.
	script := "umask 077 && tmp=$(mktemp " + Quote(path+".tmp.XXXXXX") + ") && trap 'rm -f -- \"$tmp\"' 0 && trap 'exit 1' HUP INT TERM && cat > \"$tmp\" && chmod " + Quote(fmt.Sprintf("%04o", mode.Perm())) + " \"$tmp\" && [ ! -d " + Quote(path) + " ] && mv -f -- \"$tmp\" " + Quote(path)
	_, err := s.Run(ctx, Cmd{Argv: []string{"sh", "-c", script}, Stdin: bytes.NewReader(data), Redact: []string{string(data)}})
	return err
}

func (s *sshRunner) Close(ctx context.Context) error {
	_, err := s.run(ctx, Cmd{}, []string{"-O", "exit"}, "")
	return err
}
