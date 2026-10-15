package ci_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"go.yaml.in/yaml/v3"
)

type workflowStep struct {
	Name string `yaml:"name"`
	ID   string `yaml:"id"`
	Run  string `yaml:"run"`
}

func workflowRun(t *testing.T, name, step string) string {
	t.Helper()
	tree := testutil.RenderPack(t, []string{"ci"}, map[string]any{"app_name": "todo", "modules": "web,deploy,backups"})
	data, err := fs.ReadFile(tree, name)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Runs struct {
			Steps []workflowStep `yaml:"steps"`
		} `yaml:"runs"`
		Jobs map[string]struct {
			Steps []workflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	steps := doc.Runs.Steps
	for _, job := range doc.Jobs {
		steps = append(steps, job.Steps...)
	}
	for _, s := range steps {
		if s.ID == step || s.Name == step {
			return s.Run
		}
	}
	t.Fatalf("%s has no step %q", name, step)
	return ""
}

func writeCIFile(t *testing.T, name, data string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func runCI(t *testing.T, dir, script string, env ...string) (spexec.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return spexec.OSRunner{}.Run(ctx, spexec.Cmd{Name: "bash", Args: []string{"-c", script}, Dir: dir, Env: env})
}

func TestSetupSpinup(t *testing.T) {
	t.Parallel()
	script := workflowRun(t, ".github/actions/setup-spinup/action.yml", "install")
	for _, tc := range []struct{ name, input, locked, checksum, want string }{
		{"lock", "", "1.2.3", "valid", "1.2.3"},
		{"explicit", "v1.3.0-rc.1", "dev", "valid", "1.3.0-rc.1"},
		{"latest", "latest", "dev", "valid", "1.4.0"},
		{"dev", "", "dev", "valid", ""},
		{"zero-dev", "", "0.0.0-dev", "valid", ""},
		{"alpha", "", "0.1.0-alpha.42", "valid", ""},
		{"missing", "", "", "valid", ""},
		{"invalid", "../../bad", "1.2.3", "valid", ""},
		{"checksum mismatch", "1.2.3", "dev", "mismatch", ""},
		{"archive missing from checksums", "1.2.3", "dev", "missing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			runner := filepath.Join(dir, "runner")
			if err := os.MkdirAll(runner, 0755); err != nil {
				t.Fatal(err)
			}
			writeCIFile(t, filepath.Join(dir, "spinup.lock"), fmt.Sprintf(`{"cli_version":%q}`, tc.locked), 0600)
			var archive bytes.Buffer
			gz := gzip.NewWriter(&archive)
			tw := tar.NewWriter(gz)
			binary := []byte("#!/bin/sh\nprintf 'spinup fixture\\n'\n")
			if err := tw.WriteHeader(&tar.Header{Name: "spinup", Mode: 0755, Size: int64(len(binary))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(binary); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(dir, "fixture.tar.gz")
			if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			sum := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
			if tc.checksum == "mismatch" {
				sum = strings.Repeat("0", 64)
			}
			writeCIFile(t, filepath.Join(bin, "curl"), `#!/bin/bash
set -euo pipefail
url=''
out=''
auth=false
while [ "$#" -gt 0 ]; do
 case "$1" in
  --output) out="$2"; shift 2 ;;
  --header) if [ "$2" = 'Authorization: Bearer test-token' ]; then auth=true; fi; shift 2 ;;
  https://*) url="$1"; shift ;;
  *) shift ;;
 esac
done
printf '%s\n' "$url" >> "$CURL_CALLS"
case "$url" in
 */releases/latest)
  [ "$auth" = true ]
  printf '{"tag_name":"v1.4.0"}\n'
  ;;
 */checksums.txt)
  archive="spinup_${EXPECTED_VERSION}_linux_amd64.tar.gz"
  if [ "$CHECKSUM_MODE" = missing ]; then archive=unrelated.tar.gz; fi
  printf '%s  %s\n' "$FIXTURE_SUM" "$archive" > "$out"
  ;;
 */spinup_*_linux_amd64.tar.gz) cp "$FIXTURE_ARCHIVE" "$out" ;;
 *) exit 9 ;;
esac
`, 0755)
			expected := tc.want
			if expected == "" {
				expected = "1.2.3"
			}
			result, err := runCI(t, dir, script,
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"RUNNER_TEMP="+runner, "RUNNER_OS=Linux", "RUNNER_ARCH=X64",
				"GITHUB_PATH="+filepath.Join(dir, "github-path"), "GITHUB_OUTPUT="+filepath.Join(dir, "github-output"),
				"SPINUP_VERSION="+tc.input, "GH_TOKEN=test-token", "EXPECTED_VERSION="+expected,
				"FIXTURE_ARCHIVE="+archivePath, "FIXTURE_SUM="+sum, "CHECKSUM_MODE="+tc.checksum, "CURL_CALLS="+filepath.Join(dir, "calls"))
			installed := filepath.Join(runner, "spinup/bin/spinup")
			if tc.want == "" {
				if err == nil {
					t.Fatalf("install succeeded: %s", result.Stdout)
				}
				if _, statErr := os.Stat(installed); !os.IsNotExist(statErr) {
					t.Fatalf("failed install wrote binary: %v", statErr)
				}
				if tc.checksum == "valid" {
					if !strings.Contains(string(result.Stderr), "Try:") {
						t.Errorf("missing guidance: %s", result.Stderr)
					}
					if _, statErr := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(statErr) {
						t.Error("invalid version made a download request")
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("install: %v\n%s", err, result.Stderr)
			}
			info, err := os.Stat(installed)
			if err != nil || info.Mode().Perm() != 0755 {
				t.Fatalf("installed binary mode: %v, %v", info, err)
			}
			output, err := os.ReadFile(filepath.Join(dir, "github-output"))
			if err != nil || string(output) != "version="+tc.want+"\n" {
				t.Fatalf("output=%q, error=%v", output, err)
			}
			path, err := os.ReadFile(filepath.Join(dir, "github-path"))
			if err != nil || strings.TrimSpace(string(path)) != filepath.Dir(installed) {
				t.Fatalf("path=%q, error=%v", path, err)
			}
			if bytes.Contains(append(result.Stdout, result.Stderr...), []byte("test-token")) {
				t.Error("token appeared in output")
			}
		})
	}
}

func TestUpgradeWorkflowWorktree(t *testing.T) {
	t.Parallel()
	script := workflowRun(t, ".github/workflows/upgrade-check.yml", "upgrade")
	for _, mode := range []string{"success", "conflict", "current", "failed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			runner := filepath.Join(dir, "runner")
			if err := os.MkdirAll(runner, 0755); err != nil {
				t.Fatal(err)
			}
			if result, err := runCI(t, dir, "git init -q"); err != nil {
				t.Fatalf("git init: %v: %s", err, result.Stderr)
			}
			writeCIFile(t, filepath.Join(bin, "spinup"), `#!/bin/bash
set -euo pipefail
[ -z "${SPINUP_KIT_REPLACE+x}" ]
[ "$*" = 'upgrade --yes --non-interactive' ]
case "$UPGRADE_MODE" in
 failed) exit 4 ;;
 current) exit 0 ;;
esac
work="$GITHUB_WORKSPACE/.spinup/upgrade/$SPINUP_RELEASE"
mkdir -p "$work"
printf 'upgraded\n' > "$work/changed.txt"
if [ "$UPGRADE_MODE" = conflict ]; then
 jq -n --arg work "$work" '{worktree:$work,conflicts:["web/package.json","api/main.go"]}' > .spinup/upgrade.json
 exit 3
fi
`, 0755)
			result, err := runCI(t, dir, script, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SPINUP_RELEASE=1.4.0", "SPINUP_KIT_REPLACE=/must/be/unset", "UPGRADE_MODE="+mode,
				"RUNNER_TEMP="+runner, "GITHUB_WORKSPACE="+dir, "GITHUB_OUTPUT="+filepath.Join(dir, "output"))
			if mode == "failed" {
				if err == nil || result.ExitCode != 4 {
					t.Fatalf("upgrade failure swallowed: %v: %+v", err, result)
				}
				return
			}
			if err != nil {
				t.Fatalf("upgrade: %v\n%s", err, result.Stderr)
			}
			expected := "path=" + filepath.Join(dir, ".spinup/upgrade/1.4.0") + "\n"
			if mode == "current" {
				expected = "path=.\n"
			}
			output, err := os.ReadFile(filepath.Join(dir, "output"))
			if err != nil || string(output) != expected {
				t.Fatalf("path=%q, error=%v", output, err)
			}
			body, err := os.ReadFile(filepath.Join(runner, "spinup-upgrade-body.md"))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "conflict" {
				for _, want := range []string{"<<<<<<<", "|||||||", "=======", ">>>>>>>", "web/package.json", "api/main.go"} {
					if !bytes.Contains(body, []byte(want)) {
						t.Errorf("PR body lacks %q: %s", want, body)
					}
				}
			}
		})
	}
}

func TestWorkflowKeys(t *testing.T) {
	t.Parallel()
	for _, workflow := range []string{"deploy", "backup-verify"} {
		t.Run(workflow, func(t *testing.T) {
			t.Parallel()
			script := workflowRun(t, ".github/workflows/"+workflow+".yml", "Install the CI keys")
			dir := t.TempDir()
			result, err := runCI(t, dir, script, "HOME="+dir, "RUNNER_TEMP="+dir, "GITHUB_ENV="+filepath.Join(dir, "github-env"),
				"SPINUP_ENVIRONMENT=staging", "SPINUP_APP=todo", "SPINUP_AGE_KEY=private-age-fixture", "SPINUP_SSH_KEY=private-ssh-fixture", "SPINUP_SSH_KNOWN_HOSTS=host-fixture")
			if err != nil {
				t.Fatalf("keys: %v\n%s", err, result.Stderr)
			}
			for path, want := range map[string]string{
				".config/spinup/keys/todo-staging.agekey": "private-age-fixture\n",
				".ssh/spinup_ci":   "private-ssh-fixture\n",
				".ssh/known_hosts": "host-fixture\n",
			} {
				data, err := os.ReadFile(filepath.Join(dir, path))
				if err != nil || string(data) != want {
					t.Fatalf("%s = %q, %v", path, data, err)
				}
				info, err := os.Stat(filepath.Join(dir, path))
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("%s mode: %v, %v", path, info, err)
				}
				if bytes.Contains(append(result.Stdout, result.Stderr...), []byte(strings.TrimSpace(want))) {
					t.Errorf("%s appeared in output", path)
				}
			}
			config, err := os.ReadFile(filepath.Join(dir, "spinup-ssh.conf"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(config, []byte("StrictHostKeyChecking yes")) || !bytes.Contains(config, []byte("BatchMode yes")) {
				t.Errorf("unsafe SSH config: %s", config)
			}
			cleanup := workflowRun(t, ".github/workflows/"+workflow+".yml", "Remove the CI keys")
			if result, err := runCI(t, dir, cleanup, "HOME="+dir, "RUNNER_TEMP="+dir); err != nil {
				t.Fatalf("cleanup: %v\n%s", err, result.Stderr)
			}
			if _, err := os.Stat(filepath.Join(dir, ".config/spinup/keys/todo-staging.agekey")); !os.IsNotExist(err) {
				t.Errorf("key remains: %v", err)
			}
		})
	}
}

func TestSetupSpinupShellcheck(t *testing.T) {
	t.Parallel()
	script := workflowRun(t, ".github/actions/setup-spinup/action.yml", "install")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := spexec.OSRunner{}.Run(ctx, spexec.Cmd{Name: "shellcheck", Args: []string{"--shell=bash", "-"}, Stdin: strings.NewReader(script)})
	if err != nil {
		t.Fatalf("shellcheck: %v\n%s\n%s", err, result.Stdout, result.Stderr)
	}
}
