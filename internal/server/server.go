package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"go.yaml.in/yaml/v3"
)

var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Options holds the server command flags and streams.
type Options struct {
	Dir, Env, SSHKey, Since                              string
	Plan, PrintCloudInit, JSON, Follow, AcceptNewHostKey bool
	In                                                   io.Reader
	Out, ErrOut                                          io.Writer
}

type target struct {
	root, app string
	host      remote.Host
}

func load(o Options) (target, error) {
	if err := platform(runtime.GOOS); err != nil {
		return target{}, err
	}
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return target{}, fmt.Errorf("find server project: %w\nTry: run inside a spinup app or pass --dir", err)
	}
	config, err := project.Load(root)
	if err != nil {
		return target{}, fmt.Errorf("load server configuration: %w\nTry: correct spinup.toml", err)
	}
	if !appName.MatchString(config.App.Name) {
		return target{}, fmt.Errorf("load server configuration: invalid app name\nTry: correct app.name in spinup.toml")
	}
	env, err := config.ResolveEnv(o.Env)
	if err != nil {
		return target{}, err
	}
	t := target{root: root, app: config.App.Name}
	// Printing cloud-init does not require a provisioned host.
	if !o.PrintCloudInit {
		t.host, err = remote.HostFromEnv(env)
	}
	return t, err
}

func platform(goos string) error {
	if goos == "windows" {
		return fmt.Errorf("server commands require WSL2 on Windows\nTry: run spinup server inside WSL2")
	}
	return nil
}

func output(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// Bootstrap prepares the selected host or prints its rendered cloud-init file.
func Bootstrap(ctx context.Context, o Options, runner runexec.Runner) error {
	if o.Plan && o.PrintCloudInit {
		return fmt.Errorf("bootstrap failed: --plan and --print-cloud-init cannot be combined\nTry: select one output mode")
	}
	t, err := load(o)
	if err != nil {
		return err
	}
	key, err := readPublicKey(o.SSHKey)
	if err != nil {
		return err
	}
	if o.PrintCloudInit {
		return printCloudInit(t.root, key, output(o.Out))
	}
	r := remote.New(t.host, runner, remote.Options{AcceptNewHostKey: o.AcceptNewHostKey})
	defer func() { _ = r.Close(ctx) }()
	reports, err := BootstrapHost(ctx, r, t.app, key, o.Plan)
	for _, report := range reports {
		status := "ok"
		if report.Changed {
			status = "changed"
			if report.Planned {
				status = "would-change"
			}
		}
		if !o.JSON {
			if _, e := fmt.Fprintf(output(o.Out), "%s\t%s\n", status, report.Step); e != nil {
				return fmt.Errorf("write bootstrap report: %w\nTry: check the output stream", e)
			}
		}
	}
	if o.JSON {
		if e := json.NewEncoder(output(o.Out)).Encode(reports); e != nil {
			return fmt.Errorf("write bootstrap report: %w\nTry: check the output stream", e)
		}
	}
	if err != nil {
		return err
	}
	memory := memoryCheck(ctx, &doctor.Context{Deps: map[string]any{"remote": r}})
	if memory.Status == doctor.Fail {
		return fmt.Errorf("bootstrap memory check: %s\nTry: %s", memory.Message, memory.Try)
	}
	if memory.Try != "" {
		if _, err := fmt.Fprintf(output(o.ErrOut), "%s\tvm-memory\t%s\nTry: %s\n", memory.Status, memory.Message, memory.Try); err != nil {
			return fmt.Errorf("write memory recommendation: %w\nTry: check the output stream", err)
		}
	}
	return nil
}

func readPublicKey(path string) (string, error) {
	if path == "" {
		path = "~/.ssh/id_ed25519.pub"
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find SSH key directory: %w\nTry: pass --ssh-key <path>", err)
		}
		path = filepath.Join(home, path[2:])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read operator SSH key: %w\nTry: pass --ssh-key <public-key-file>", err)
	}
	key := strings.TrimSpace(string(data))
	fields := strings.Fields(key)
	if len(fields) < 2 || strings.ContainsAny(key, "\r\n\x00") {
		return "", invalidKey()
	}
	switch fields[0] {
	case "ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
	default:
		return "", invalidKey()
	}
	if data, err := base64.StdEncoding.DecodeString(fields[1]); err != nil || !publicKeyWire(fields[0], data) {
		return "", invalidKey()
	}
	return key, nil
}

func invalidKey() error {
	return fmt.Errorf("read operator SSH key: expected one public key without authorized_keys options\nTry: pass --ssh-key <public-key-file>")
}

// ReadPublicKey reads one operator public key from path ("" means ~/.ssh/id_ed25519.pub).
func ReadPublicKey(path string) (string, error) { return readPublicKey(path) }

// CloudInit returns the rendered deploy/cloud-init/user-data.yaml of the app at root, with the
// deploy user's public key in place of the SPINUP_DEPLOY_SSH_KEY placeholder.
func CloudInit(root, key string) ([]byte, error) {
	var b bytes.Buffer
	if err := printCloudInit(root, key, &b); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func printCloudInit(root, key string, out io.Writer) error {
	data, err := os.ReadFile(filepath.Join(root, "deploy", "cloud-init", "user-data.yaml"))
	if err != nil {
		return fmt.Errorf("read rendered cloud-init: %w\nTry: add the deploy pack to the app", err)
	}
	if !strings.Contains(string(data), "- SPINUP_DEPLOY_SSH_KEY") {
		return fmt.Errorf("render cloud-init: SSH key placeholder is missing\nTry: restore SPINUP_DEPLOY_SSH_KEY in deploy/cloud-init/user-data.yaml")
	}
	// A YAML scalar prevents a key comment from changing the document structure.
	scalar, err := yaml.Marshal(key)
	if err != nil {
		return fmt.Errorf("render cloud-init SSH key: %w\nTry: check the public key", err)
	}
	rendered := strings.ReplaceAll(string(data), "- SPINUP_DEPLOY_SSH_KEY", "- "+strings.TrimSpace(string(scalar)))
	if _, err := io.WriteString(out, rendered); err != nil {
		return fmt.Errorf("write cloud-init: %w\nTry: check the output stream", err)
	}
	return nil
}
