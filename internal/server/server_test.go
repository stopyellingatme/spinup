package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
	"go.yaml.in/yaml/v3"
)

const operatorKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA operator"

func TestReadPublicKey(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, key string
		valid     bool
	}{
		{"valid", operatorKey + "\n", true},
		{"comment", operatorKey + " : 'quoted' # comment", true},
		{"empty", "", false},
		{"private", "-----BEGIN OPENSSH PRIVATE KEY-----\ncontent", false},
		{"multiple", operatorKey + "\n" + operatorKey, false},
		{"options", "command=bad " + operatorKey, false},
		{"bad base64", "ssh-ed25519 !bad", false},
		{"truncated wire", "ssh-ed25519 AAAA", false},
		{"mismatch", strings.Replace(operatorKey, "ssh-ed25519", "ssh-rsa", 1), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "key.pub")
			if err := os.WriteFile(path, []byte(tt.key), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := readPublicKey(path)
			if (err == nil) != tt.valid {
				t.Fatalf("key=%q error=%v", got, err)
			}
		})
	}
}

func TestCloudInitScalar(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "cloud-init"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "deploy", "cloud-init", "user-data.yaml")
	data := "#cloud-config\nusers:\n  - name: deploy\n    ssh_authorized_keys:\n      - SPINUP_DEPLOY_SSH_KEY\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	key := operatorKey + " : comment # with quotes ' and \""
	var out bytes.Buffer
	if err := printCloudInit(root, key, &out); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Users []struct {
			Keys []string `yaml:"ssh_authorized_keys"`
		}
	}
	if err := yaml.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Users) != 1 || !reflect.DeepEqual(parsed.Users[0].Keys, []string{key}) {
		t.Fatalf("rendered key changed: %s", out.String())
	}
	original, err := os.ReadFile(path)
	if err != nil || string(original) != data {
		t.Fatal("print changed local file", err)
	}
}

func TestAuditOutput(t *testing.T) {
	t.Parallel()
	for _, structured := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[structured], func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			results := []CheckResult{check("literal-secret", "api", "A secret value is present.", "Use _FILE.", false)}
			err := writeAudit(results, Options{JSON: structured, Out: &out})
			if ui.ExitCode(err) != ui.ExitError {
				t.Fatal(err)
			}
			if structured {
				var got []CheckResult
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, results) {
					t.Fatalf("output=%s error=%v", out.String(), err)
				}
			} else if !strings.Contains(out.String(), "fail\tliteral-secret:api") || !strings.Contains(out.String(), "Try: Use _FILE.") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestBootstrapStopsOnFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"sudo", "transport", "apply"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("cat", "/etc/os-release").Return("ID=debian\n", "", 0)
			if failure == "sudo" {
				f.Expect("sudo", "-n", "true").Return("", "not permitted", 1)
			} else {
				f.Expect("sudo", "-n", "true").Return("", "", 0)
				if failure == "transport" {
					f.Expect("sudo", "-n", "sh").Fail(errors.New("connection lost"))
				} else {
					f.Expect("sudo", "-n", "sh").Return("", "", 1)
					f.Expect("sudo", "-n", "sh").Return("", "installation failed", 1)
				}
			}
			if _, err := BootstrapHost(context.Background(), f, "todo", operatorKey, false); err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatal(err)
			}
			f.AssertDone(t)
		})
	}
}

func TestSSHEndToEndArgv(t *testing.T) {
	// os.UserCacheDir uses HOME on macOS and XDG_CACHE_HOME on Linux.
	cache, err := os.MkdirTemp("/tmp", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cache) })
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("SPINUP_SSH_CONFIG", "/tmp/test-ssh-config")
	root := t.TempDir()
	config := "[app]\nname='todo'\n[env.staging]\nhost='vm'\nuser='deploy'\nport=2222\n"
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	f := &runexec.Fake{}
	f.Expect("ssh", "-t").Return("shell-output", "", 0)
	f.Expect("ssh", "-F").Return("", "", 0)
	var out bytes.Buffer
	input := strings.NewReader("exit\n")
	if err := SSH(context.Background(), Options{Dir: root, Env: "staging", In: input, Out: &out}, f); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	c := calls[0]
	joined := strings.Join(c.Args, " ")
	for _, part := range []string{"-F /tmp/test-ssh-config", "-p 2222", "ControlMaster=auto", "ControlPersist=60s", "-- deploy@vm", "'sh' '-c'"} {
		if !strings.Contains(joined, part) {
			t.Fatalf("missing %s: %s", part, joined)
		}
	}
	if !c.Interactive || c.Stdin != input || out.String() != "shell-output" || calls[1].Interactive {
		t.Fatal(c)
	}
	f.AssertDone(t)
}

func TestPlatform(t *testing.T) {
	t.Parallel()
	if err := platform("windows"); err == nil || !strings.Contains(err.Error(), "WSL2") || !strings.Contains(err.Error(), "Try:") {
		t.Fatal(err)
	}
	if err := platform("linux"); err != nil {
		t.Fatal(err)
	}
}

func TestPublicKeyWireTruncation(t *testing.T) {
	t.Parallel()
	fields := strings.Fields(operatorKey)
	data, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(data); i++ {
		if publicKeyWire(fields[0], data[:i]) {
			t.Fatalf("accepted %d bytes", i)
		}
	}
	if publicKeyWire(fields[0], append(data, 0)) {
		t.Fatal("accepted trailing bytes")
	}
	var huge [4]byte
	binary.BigEndian.PutUint32(huge[:], ^uint32(0))
	if publicKeyWire(fields[0], huge[:]) {
		t.Fatal("accepted oversized string")
	}
}

func TestBootstrapPlanCommand(t *testing.T) {
	cache, err := os.MkdirTemp("/tmp", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cache) })
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("SPINUP_SSH_CONFIG", "")
	if err := os.MkdirAll(filepath.Join(cache, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, ".ssh", "id_ed25519.pub"), []byte(operatorKey), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("[app]\nname='todo'\n[env.staging]\nhost='vm'\nuser='ubuntu'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &runexec.Fake{}
	f.Expect("ssh").Return("ID=ubuntu\n", "", 0)
	f.Expect("ssh").Return("", "", 0)
	for range bootstrapSteps("todo", operatorKey) {
		f.Expect("ssh").Return("", "", 1)
	}
	f.Expect("ssh").Return("MemTotal: 1048576 kB\n", "", 0)
	f.Expect("ssh").Return("", "", 0)
	var out, diagnostics bytes.Buffer
	if err := Bootstrap(context.Background(), Options{Dir: root, Env: "staging", Plan: true, Out: &out, ErrOut: &diagnostics}, f); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "would-change") != len(bootstrapSteps("todo", operatorKey)) || !strings.Contains(diagnostics.String(), "lite") || !strings.Contains(diagnostics.String(), "warn") {
		t.Fatalf("stdout=%s stderr=%s", out.String(), diagnostics.String())
	}
	for _, c := range f.Calls() {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, "apt-get install") || strings.Contains(joined, "systemctl reload") || strings.Contains(joined, "curl -fsSL") {
			t.Fatal("plan applied a change", joined)
		}
	}
	f.AssertDone(t)
}
