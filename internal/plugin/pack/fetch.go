package pack

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	execx "github.com/stopyellingatme/spinup/internal/exec"
)

func (m *Manager) fetch(ctx context.Context, source, version, name string) (tree, error) {
	if !token(source) {
		return tree{}, problem("fetch plugin", fmt.Errorf("invalid source %q", source))
	}
	if m.Offline {
		cfg, lock, err := m.state()
		if err != nil {
			return tree{}, err
		}
		for _, decl := range cfg.Plugins {
			if decl.Source != source || (name != "" && decl.Name != name) {
				continue
			}
			for _, entry := range lock.Plugins {
				if entry.Name == decl.Name && (version == "" || sameVersion(entry.Version, version)) {
					return m.cached(entry.Name, entry.Version, source, entry.SHA256)
				}
			}
		}
		return tree{}, problem("read plugin cache", fmt.Errorf("no current project approval for %q at %q", source, version))
	}
	if local, ok := strings.CutPrefix(source, "path:"); ok {
		if local == "" {
			return tree{}, problem("fetch plugin", fmt.Errorf("path source is empty"))
		}
		if !filepath.IsAbs(local) {
			root := m.PathRoot
			if root == "" {
				root = m.Root
			}
			local = filepath.Join(root, local)
		}
		return readTree(filepath.Clean(local))
	}
	remote, ok := strings.CutPrefix(source, "git:")
	if !ok {
		return tree{}, problem("fetch plugin", fmt.Errorf("source must use a path: or git: prefix"))
	}
	u, err := url.Parse(remote)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "ssh" && u.Scheme != "git") || u.RawQuery != "" || u.Fragment != "" {
		return tree{}, problem("fetch plugin", fmt.Errorf("git source must be an https, ssh, or git URL"))
	}
	if !safeVersion(version) {
		return tree{}, problem("fetch plugin", fmt.Errorf("git requires --version with a fixed tag"))
	}
	temp, err := os.MkdirTemp("", "spinup-plugin-fetch-")
	if err != nil {
		return tree{}, problem("create plugin fetch directory", err)
	}
	defer os.RemoveAll(temp)
	runner := m.Runner
	if runner == nil {
		runner = execx.OSRunner{}
	}
	// An empty template directory disables user templates and their executable hooks.
	// Disable recursive fetches, user configuration, and inherited git commands.
	gitEnv := []string{
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_COUNT=0",
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_SSH_COMMAND=ssh -oBatchMode=yes",
		"GIT_DIR=", "GIT_WORK_TREE=", "GIT_CONFIG=", "GIT_CONFIG_PARAMETERS=", "GIT_TEMPLATE_DIR=",
		"GIT_COMMON_DIR=", "GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_INDEX_FILE=",
	}
	_, err = runFetchGit(ctx, runner, execx.Cmd{Name: "git", Args: []string{
		"-c", "protocol.file.allow=never", "-c", "protocol.ext.allow=never",
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always",
		"-c", "protocol.ssh.allow=always", "-c", "protocol.git.allow=always",
		"-c", "core.hooksPath=" + filepath.Join(temp, "no-hooks"),
		"clone", "--depth", "1", "--branch", version, "--single-branch", "--no-recurse-submodules", "--template=", "--filter=blob:limit=5m",
		"--", remote, filepath.Join(temp, "repo"),
	}, Dir: temp, Timeout: 2 * time.Minute, Env: gitEnv})
	if err != nil {
		return tree{}, problem("fetch git plugin", err)
	}
	repo := filepath.Join(temp, "repo")
	result, err := runFetchGit(ctx, runner, execx.Cmd{Name: "git", Args: []string{"-c", "protocol.file.allow=never", "-C", repo, "show-ref", "--head", "--tags", "--dereference"}, Dir: temp, Timeout: 30 * time.Second, Env: gitEnv})
	if err != nil {
		return tree{}, problem("verify plugin tag", err)
	}
	if err := verifyTag(result.Stdout, version); err != nil {
		return tree{}, err
	}
	index, err := runFetchGit(ctx, runner, execx.Cmd{Name: "git", Args: []string{"-c", "protocol.file.allow=never", "-C", repo, "ls-files", "--stage", "-z"}, Dir: temp, Timeout: 30 * time.Second, Env: gitEnv})
	if err != nil {
		return tree{}, problem("inspect plugin git index", err)
	}
	for _, entry := range strings.Split(string(index.Stdout), "\x00") {
		if strings.HasPrefix(entry, "160000 ") {
			return tree{}, problem("validate plugin tree", fmt.Errorf("git submodules are forbidden"))
		}
	}
	return readTree(repo)
}

func verifyTag(output []byte, tag string) error {
	var head, direct, peeled string
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[1] {
		case "HEAD":
			head = fields[0]
		case "refs/tags/" + tag:
			direct = fields[0]
		case "refs/tags/" + tag + "^{}":
			peeled = fields[0]
		}
	}
	if peeled != "" {
		direct = peeled
	}
	if head == "" || direct == "" || head != direct {
		return problem("verify plugin tag", fmt.Errorf("version %q is not a tag at the fetched commit", tag))
	}
	return nil
}
