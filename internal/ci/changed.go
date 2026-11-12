package ci

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	spexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/services"
)

// allServicesFiles are the paths whose change rebuilds every service.
var allServicesFiles = map[string]bool{
	"spinup.toml": true,
	"spinup.lock": true,
	"go.mod":      true,
	"go.work":     true,
	"api/go.mod":  true,
}

// touchesAllServices reports whether a changed path affects every service:
// the project declaration and lock, the workflows and actions, the Go module files,
// and the compose files.
func touchesAllServices(file string) bool {
	if allServicesFiles[file] || strings.HasPrefix(file, ".github/") {
		return true
	}
	base := path.Base(file)
	ext := path.Ext(base)
	if ext != ".yml" && ext != ".yaml" {
		return false
	}
	return strings.HasPrefix(base, "compose") || strings.HasPrefix(base, "docker-compose")
}

// FilterChanged keeps the services that a list of changed paths affects.
// It keeps all services when a path touches a shared file (see touchesAllServices).
// The order of the input is kept.
func FilterChanged(doc MatrixDoc, changed []string) MatrixDoc {
	for _, file := range changed {
		if touchesAllServices(file) {
			return doc
		}
	}
	out := MatrixDoc{Include: make([]MatrixEntry, 0, len(doc.Include))}
	for _, entry := range doc.Include {
		prefix := services.ContextDir(entry.Name) + "/"
		for _, file := range changed {
			if strings.HasPrefix(file, prefix) {
				out.Include = append(out.Include, entry)
				break
			}
		}
	}
	return out
}

// MatrixChanged is like Matrix, but keeps a service only when the output of
// "git diff --name-only -z <ref>...HEAD" changed it. If git fails, it writes a
// warning to errOut and returns every service (fail open). A nil runner uses exec.OSRunner.
func MatrixChanged(ctx context.Context, runner spexec.Runner, root, ref string, errOut io.Writer) (MatrixDoc, error) {
	doc, err := Matrix(root)
	if err != nil {
		return MatrixDoc{}, err
	}
	if errOut == nil {
		errOut = io.Discard
	}
	if runner == nil {
		runner = spexec.OSRunner{}
	}
	result, err := runner.Run(ctx, spexec.Cmd{Name: "git", Args: []string{"diff", "--name-only", "-z", ref + "...HEAD"}, Dir: root})
	if err == nil && result.ExitCode != 0 {
		err = fmt.Errorf("git diff returned exit code %d: %s", result.ExitCode, strings.TrimSpace(string(result.Stderr)))
	}
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "warning: cannot find the changed files since %s: %v\nTry: fetch the full history (fetch-depth: 0) and check the ref. Keeping all services.\n", ref, err)
		return doc, nil
	}
	// -z separates the paths with NUL and does not quote non-ASCII names.
	var changed []string
	for _, name := range strings.Split(string(result.Stdout), "\x00") {
		if name = strings.TrimSpace(name); name != "" {
			changed = append(changed, name)
		}
	}
	return FilterChanged(doc, changed), nil
}
