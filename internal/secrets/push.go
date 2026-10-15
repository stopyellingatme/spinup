package secrets

import (
	"context"
	"io/fs"
	"path"
	"strings"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

// Uploader uploads private bytes and runs commands on a remote host.
// Adapt remote command fields to exec.Cmd when connecting the SSH runner.
type Uploader interface {
	Upload(context.Context, []byte, string, fs.FileMode) error
	Run(context.Context, runexec.Cmd) (runexec.Result, error)
}

// Push replaces secret files and removes names absent from values.
// Callers must hold the deployment lock while synchronizing this directory.
func Push(ctx context.Context, r Uploader, app string, values map[string]string) error {
	if !appPattern.MatchString(app) {
		return problem("push secrets", "the application name is invalid", "use a valid application name")
	}
	for name := range values {
		if !namePattern.MatchString(name) {
			return problem("push secrets", "a secret name is invalid", "use uppercase environment variable names")
		}
	}
	if r == nil {
		return problem("push secrets", "the remote runner is missing", "configure the remote runner")
	}
	base := path.Join("/opt", app, "secrets")
	// This script is constant. All paths are separate arguments.
	prepare := `test ! -L "$1" && mkdir -p -- "$1" && chmod 700 -- "$1"`
	if _, err := r.Run(ctx, runexec.Cmd{Name: "sh", Args: []string{"-c", prepare, "spinup-secrets", base}}); err != nil {
		return problem("push secrets", "the remote directory could not be secured", "check the deploy user and remote directory permissions")
	}
	result, err := r.Run(ctx, runexec.Cmd{Name: "find", Args: []string{base, "-mindepth", "1", "-maxdepth", "1", "-print"}})
	if err != nil {
		return problem("push secrets", "remote names could not be read", "check remote directory permissions")
	}
	var stale []string
	for _, file := range strings.Split(strings.TrimSuffix(string(result.Stdout), "\n"), "\n") {
		if file == "" {
			continue
		}
		name := strings.TrimPrefix(file, base+"/")
		if !namePattern.MatchString(name) || file != path.Join(base, name) {
			return problem("push secrets", "the remote directory contains an unexpected entry", "inspect the remote secrets directory")
		}
		if _, exists := values[name]; !exists {
			stale = append(stale, file)
		}
	}
	for _, name := range sortedNames(values) {
		if err = r.Upload(ctx, []byte(values[name]), path.Join(base, name), 0600); err != nil {
			return problem("push secrets", "a secret file could not be uploaded", "check the SSH connection and remote permissions")
		}
	}
	if len(stale) > 0 {
		if _, err = r.Run(ctx, runexec.Cmd{Name: "rm", Args: append([]string{"-f", "--"}, stale...)}); err != nil {
			return problem("push secrets", "obsolete secret files could not be removed", "check remote directory permissions")
		}
	}
	return nil
}
