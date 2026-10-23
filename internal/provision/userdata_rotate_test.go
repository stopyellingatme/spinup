package provision

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const rotateSample = `#cloud-config
write_files:
  - path: /etc/a
    content: |
      a
runcmd:
  - [sh, -ec, "true"]
`

func parseDoc(t *testing.T, s string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatal(err)
	}
	return &doc
}

func renderDoc(t *testing.T, doc *yaml.Node) string {
	t.Helper()
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func checkTooling(t *testing.T, src string) {
	t.Helper()
	doc := parseDoc(t, src)
	if err := addRotationTooling(doc, "todo"); err != nil {
		t.Fatal(err)
	}
	out := renderDoc(t, doc)
	var back struct {
		WriteFiles []struct {
			Path        string `yaml:"path"`
			Owner       string `yaml:"owner"`
			Permissions string `yaml:"permissions"`
			Content     string `yaml:"content"`
		} `yaml:"write_files"`
		Runcmd [][]string `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
	var script, rule bool
	for _, f := range back.WriteFiles {
		switch f.Path {
		case hostKeyTool:
			script = f.Content == hostKeyScript && f.Permissions == "0755" && f.Owner == "root:root"
		case "/run/spinup-todo-hostkey-sudoers":
			rule = f.Permissions == "0440" && strings.HasPrefix(f.Content, "deploy ALL=(root) NOPASSWD: /usr/local/sbin/spinup-host-key next,") && strings.HasSuffix(f.Content, "status\n")
		}
	}
	if !script || !rule {
		t.Fatalf("script=%v rule=%v in:\n%s", script, rule, out)
	}
	want := "visudo -cf /run/spinup-todo-hostkey-sudoers && mv -f /run/spinup-todo-hostkey-sudoers /etc/sudoers.d/spinup-todo-hostkey"
	n := 0
	for _, c := range back.Runcmd {
		if len(c) == 3 && c[2] == want {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("runcmd entry count %d", n)
	}
	if err := addRotationTooling(doc, "todo"); err != nil {
		t.Fatal(err)
	}
	if again := renderDoc(t, doc); again != out {
		t.Fatalf("second call changed the document")
	}
}

func TestRotationToolingSample(t *testing.T) { checkTooling(t, rotateSample) }

func TestRotationToolingMissingKeys(t *testing.T) { checkTooling(t, "#cloud-config\npackages: [a]\n") }

func TestRotationToolingGolden(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "deploy", "deploy", "cloud-init", "user-data.yaml"))
	if err != nil {
		t.Skip("no golden user data")
	}
	checkTooling(t, string(b))
}

func TestRotationToolingErrors(t *testing.T) {
	for _, app := range []string{"", "Todo", "1x", "a b", "a;b"} {
		if err := addRotationTooling(parseDoc(t, rotateSample), app); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("app %q: err %v", app, err)
		}
	}
	for _, src := range []string{"- a\n", "write_files: x\n", "runcmd: {a: b}\n", "a: b\nwrite_files: {x: y}\n"} {
		if err := addRotationTooling(parseDoc(t, src), "todo"); err == nil {
			t.Errorf("shape %q: no error", src)
		}
	}
	if err := addRotationTooling(&yaml.Node{Kind: yaml.MappingNode}, "todo"); err == nil {
		t.Error("non-document: no error")
	}
	if err := addRotationTooling(nil, "todo"); err == nil {
		t.Error("nil: no error")
	}
}

func TestRotationScript(t *testing.T) {
	for _, bin := range []string{"sh", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s missing", bin)
		}
	}
	dir := t.TempDir()
	etc, state := filepath.Join(dir, "etc"), filepath.Join(dir, "state")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "tool.sh")
	if err := os.WriteFile(script, []byte(hostKeyScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "old", "-f", filepath.Join(etc, "ssh_host_ed25519_key")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	run := func(arg string) (string, int) {
		c := exec.CommandContext(t.Context(), "sh", script, arg)
		c.Env = append(os.Environ(), "SPINUP_HOSTKEY_ETC="+etc, "SPINUP_HOSTKEY_STATE="+state, "SPINUP_HOSTKEY_RELOAD=true")
		var so bytes.Buffer
		c.Stdout = &so
		code := 0
		if err := c.Run(); err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatal(err)
			}
			code = ee.ExitCode()
		}
		return strings.TrimSpace(so.String()), code
	}
	line := func(s string) {
		t.Helper()
		if _, err := parseHostKey(s); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
	}

	if out, _ := run("status"); out != "pending" {
		t.Fatalf("status %q", out)
	}
	old, _ := run("activate") // no .next: prints the current key and changes nothing
	line(old)
	if out, _ := run("status"); out != "pending" {
		t.Fatalf("status after plain activate %q", out)
	}
	next, _ := run("next")
	line(next)
	if again, _ := run("next"); again != next {
		t.Fatal("next is not idempotent")
	}
	if cur, _ := run("activate"); cur != next {
		t.Fatalf("activate printed %q, want %q", cur, next)
	}
	if _, err := os.Stat(filepath.Join(etc, "ssh_host_ed25519_key.next")); err == nil {
		t.Fatal(".next left behind")
	}
	if out, _ := run("status"); out != "rotated" {
		t.Fatalf("status %q", out)
	}
	if cur, _ := run("activate"); cur != next {
		t.Fatal("second activate changed the key")
	}
	if pub, _ := os.ReadFile(filepath.Join(etc, "ssh_host_ed25519_key.pub")); !strings.HasPrefix(string(pub), next) {
		t.Fatal("public key file not replaced")
	}
	if _, code := run("bogus"); code != 2 {
		t.Fatalf("exit code %d", code)
	}
}
