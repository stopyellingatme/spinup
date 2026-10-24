// Package ops runs the database commands for a spinup project.
//
// Migration commands (status, redo, to) run the app binary, like
// `spinup db migrate` (ADR 0005 §1). The development database commands
// (psql, dump, restore, seed, reset, doctor) reach the postgres service of
// deploy/compose.dev.yml through `docker compose exec`. Every subprocess goes
// through internal/exec, and DryRun prints the commands instead.
package ops

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// ComposeFile is the development compose file, relative to the project root.
const ComposeFile = "deploy/compose.dev.yml"

// The development database user and database name in ComposeFile.
const (
	devUser     = "app"
	devDatabase = "app"
)

// startDB is the hint that starts the development database.
const startDB = "docker compose -f " + ComposeFile + " up -d"

// Options controls one database command and its guards.
type Options struct {
	// Dir is the directory where the search for the project root starts.
	Dir string
	// Env is the value of --env. Empty means the local development database.
	Env string
	// IKnow acknowledges a command that can lose data (redo, to).
	IKnow bool
	// Interactive allows prompts.
	Interactive bool
	// Yes answers yes to the confirmation prompts.
	Yes bool
	// CI is true when the CI environment variable is set.
	CI bool
	// ConfirmName is the value of --confirm-env.
	ConfirmName string
	// DryRun prints the commands without running them. Guards still apply.
	DryRun bool
	// In is the prompt input and the stdin of interactive commands.
	In io.Reader
	// Out and ErrOut receive the command output.
	Out, ErrOut io.Writer
}

func (o Options) confirm() project.Confirm {
	return project.Confirm{In: o.In, Out: o.Out, Interactive: o.Interactive, Yes: o.Yes, CI: o.CI, Name: o.ConfirmName}
}

// loadProject finds the project root and reads spinup.toml.
func loadProject(o Options, what string) (string, *project.Config, error) {
	root, err := project.FindRoot(o.Dir)
	if err != nil {
		return "", nil, opError(what, err)
	}
	config, err := project.Load(root)
	if err != nil {
		return "", nil, opError(what, err)
	}
	return root, config, nil
}

// guardMigration is the guard of the migration commands that can lose data.
// It resolves the environment exactly like `db migrate down` and calls
// project.ConfirmProtected when the environment is protected.
func guardMigration(o Options, config *project.Config, what, action string) error {
	if len(config.Envs) == 0 && o.Env == "" {
		return nil
	}
	env, err := config.ResolveEnv(o.Env)
	if err != nil {
		return opError(what, err)
	}
	if err := project.ConfirmProtected(env, action, o.confirm()); err != nil {
		return opError(what, err)
	}
	return nil
}

// localEnvNames are the --env values that mean the local development database.
var localEnvNames = map[string]bool{"local": true, "dev": true, "development": true}

// guardLocal refuses an --env value that names a remote environment,
// because the compose commands reach only the local development database.
// When destructive is true and spinup.toml declares the environment, it
// calls project.ConfirmProtected.
func guardLocal(o Options, config *project.Config, what, action string, destructive bool) error {
	if o.Env == "" {
		return nil
	}
	if !localEnvNames[o.Env] {
		return &ui.Error{
			What: what,
			Why:  fmt.Sprintf("spinup %s works only on the local development database, and --env names %q", action, o.Env),
			Try:  "omit --env to use the postgres service in " + ComposeFile,
		}
	}
	env, ok := config.Envs[o.Env]
	if !ok || !destructive {
		return nil
	}
	env.Name = o.Env
	if err := project.ConfirmProtected(env, action, o.confirm()); err != nil {
		return opError(what, err)
	}
	return nil
}

// confirmYes asks a yes or no question in a terminal, or needs --yes.
func confirmYes(o Options, what, question, try string) error {
	if o.Yes {
		return nil
	}
	if !o.Interactive || o.In == nil {
		return &ui.Error{What: what, Why: "this needs confirmation, and spinup cannot prompt without a terminal", Try: try}
	}
	if _, err := fmt.Fprintf(o.Out, "%s [y/N] ", question); err != nil {
		return opError(what, err)
	}
	answer, err := bufio.NewReader(o.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return opError(what, err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return &ui.Error{What: what, Why: "you did not confirm", Try: try}
}

// composeExec returns `docker compose -f deploy/compose.dev.yml exec [-T] postgres <args>`.
// When tty is false, it adds -T, so the command uses plain streams.
func composeExec(root string, tty bool, args ...string) run.Cmd {
	a := []string{"compose", "-f", ComposeFile, "exec"}
	if !tty {
		a = append(a, "-T")
	}
	a = append(a, "postgres")
	a = append(a, args...)
	return run.Cmd{Name: "docker", Args: a, Dir: root}
}

// psqlArgs returns the psql command line for the development database.
func psqlArgs(extra ...string) []string {
	return append([]string{"psql", "-U", devUser, "-d", devDatabase}, extra...)
}

// appMigrate returns `go run ./cmd/api migrate <args>` in api/.
func appMigrate(root string, args ...string) run.Cmd {
	return run.Cmd{Name: "go", Args: append([]string{"run", "./cmd/api", "migrate"}, args...), Dir: filepath.Join(root, "api")}
}

// opError turns err into a user-facing error for the operation what.
func opError(what string, err error) error {
	var ue *ui.Error
	if errors.As(err, &ue) {
		return err
	}
	code := ui.ExitError
	why := err.Error()
	try := "check the command output, then retry"
	var exit *run.ExitError
	var missing *run.NotFoundError
	switch {
	case errors.As(err, &missing) && missing.Name == "docker":
		why = "the docker command was not found"
		try = "install Docker, then start the database: " + startDB
	case errors.As(err, &missing):
		why = fmt.Sprintf("the %s command was not found", missing.Name)
		try = "install " + missing.Name + ", or run spinup doctor"
	case errors.As(err, &exit):
		code = exit.Code
		why = fmt.Sprintf("%s exited with status %d", exit.Cmd, exit.Code)
		if strings.HasPrefix(exit.Cmd, "docker ") {
			try = "check the output above. If the database is not running, start it: " + startDB
		}
	default:
		// Errors from internal/project carry their own "Try:" hint. Show it once.
		if before, after, ok := strings.Cut(why, "Try:"); ok {
			why = strings.TrimRight(strings.TrimSpace(before), ".")
			try = strings.TrimSpace(after)
		}
	}
	return &ui.Error{What: what, Why: why, Try: try, Code: code, Err: err}
}

// printCmd writes the command line of c, plus an optional redirection.
func printCmd(w io.Writer, c run.Cmd, redirect string) error {
	line := c.String()
	if redirect != "" {
		line += " " + redirect
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

// quotePath quotes a path for a shell redirection in a printed command.
func quotePath(p string) string {
	return (run.Cmd{Name: p}).String()
}
