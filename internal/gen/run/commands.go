package run

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// validateCommands checks the external commands of a generator: each has a
// program name and runs in a clean directory inside the project.
func validateCommands(name string, commands []gen.Command) ([]gen.Command, error) {
	var valid []gen.Command
	for _, c := range commands {
		dir := c.Dir
		if dir == "" {
			dir = "."
		}
		clean := dir
		if dir != "." {
			var err error
			if clean, err = vfs.CleanPath(dir); err != nil || clean != dir {
				return nil, fmt.Errorf("generator %q asks to run a command in %q: the directory must be a clean path inside the project\nTry: repair the generator commands", name, c.Dir)
			}
		}
		if len(c.Argv) == 0 || c.Argv[0] == "" {
			return nil, fmt.Errorf("generator %q asks to run a command without a program name\nTry: repair the generator commands", name)
		}
		valid = append(valid, gen.Command{Dir: clean, Argv: append([]string(nil), c.Argv...)})
	}
	return valid, nil
}

// commandLine returns the shell-quoted command line of c.
func commandLine(c gen.Command) string {
	return exec.Cmd{Name: c.Argv[0], Args: c.Argv[1:]}.String()
}

// previewCommands writes one "run" line for each command, for --dry-run and --diff.
func previewCommands(out io.Writer, commands []gen.Command) error {
	for _, c := range commands {
		if _, err := fmt.Fprintf(out, "run %s (in %s)\n", commandLine(c), c.Dir); err != nil {
			return withHint("write generator preview", err, "check the output destination")
		}
	}
	return nil
}

// runCommands runs the commands in order through r, in <root>/<Dir>. The
// command output goes to out. The first failure stops the run. The files that
// the generator wrote stay on disk.
func runCommands(ctx context.Context, root string, commands []gen.Command, r exec.Runner, out io.Writer) error {
	for _, c := range commands {
		result, err := r.Run(ctx, exec.Cmd{Name: c.Argv[0], Args: c.Argv[1:], Dir: filepath.Join(root, filepath.FromSlash(c.Dir)), Stdout: out, Stderr: out})
		if err == nil && result.ExitCode != 0 {
			err = fmt.Errorf("exit code %d", result.ExitCode)
		}
		if err != nil {
			return fmt.Errorf("generator command %s failed in %s: %s\nTry: correct the problem, then run %s in %s by hand (the generated files are already written)", commandLine(c), c.Dir, reason(err), commandLine(c), c.Dir)
		}
	}
	return nil
}

// reason returns the error text without its Try lines, so that the command
// error has one Try line.
func reason(err error) string {
	var lines []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Try:") {
			continue
		}
		lines = append(lines, line)
	}
	text := strings.Join(lines, "\n")
	if i := strings.Index(text, " Try: "); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}
