package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// DefaultDumpFile is the dump file of `spinup db dump`, relative to the project root.
const DefaultDumpFile = ".spinup/dumps/app.dump"

// SeedsDir is the seed directory, relative to the project root.
const SeedsDir = "api/db/seeds"

// resetSQL drops and recreates the public schema.
const resetSQL = "DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;"

// Psql opens an interactive psql session on the development database. The
// process keeps the terminal until psql exits.
func Psql(ctx context.Context, o Options, runner run.Runner) error {
	const what = "Cannot open psql"
	root, config, err := loadProject(o, what)
	if err != nil {
		return err
	}
	if err := guardLocal(o, config, what, "db psql", false); err != nil {
		return err
	}
	c := composeExec(root, true, psqlArgs()...)
	if o.DryRun {
		return printCmd(o.Out, c, "")
	}
	c.Interactive = true
	c.Stdin, c.Stdout, c.Stderr = o.In, o.Out, o.ErrOut
	if _, err := runner.Run(ctx, c); err != nil {
		return opError(what, err)
	}
	return nil
}

// Dump writes a pg_dump custom-format archive of the development database
// to file (DefaultDumpFile under the project root when file is empty). It
// streams the output of pg_dump to a temporary file and renames it on success.
func Dump(ctx context.Context, o Options, runner run.Runner, file string) error {
	const what = "Cannot dump the database"
	root, config, err := loadProject(o, what)
	if err != nil {
		return err
	}
	if err := guardLocal(o, config, what, "db dump", false); err != nil {
		return err
	}
	shown := file
	if file == "" {
		shown = DefaultDumpFile
		file = filepath.Join(root, filepath.FromSlash(DefaultDumpFile))
	}
	c := composeExec(root, false, "pg_dump", "-U", devUser, "-d", devDatabase, "-Fc")
	if o.DryRun {
		return printCmd(o.Out, c, "> "+quotePath(shown))
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return opError(what, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".dump-*")
	if err != nil {
		return opError(what, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	c.Stdout, c.Stderr = tmp, o.ErrOut
	_, runErr := runner.Run(ctx, c)
	closeErr := tmp.Close()
	if runErr != nil {
		return opError(what, runErr)
	}
	if closeErr != nil {
		return opError(what, closeErr)
	}
	if err := os.Rename(tmp.Name(), file); err != nil {
		return opError(what, err)
	}
	_, err = fmt.Fprintf(o.Out, "Wrote %s\n", shown)
	return err
}

// Restore loads a pg_dump custom-format archive into the development
// database with `pg_restore --clean --if-exists`. It has the guards of Reset.
func Restore(ctx context.Context, o Options, runner run.Runner, file string) error {
	const what = "Cannot restore the database"
	root, config, err := loadProject(o, what)
	if err != nil {
		return err
	}
	if err := guardLocal(o, config, what, "db restore", true); err != nil {
		return err
	}
	info, err := os.Stat(file)
	if err != nil || info.IsDir() {
		return &ui.Error{What: what, Why: fmt.Sprintf("%s is not a dump file", file), Try: "spinup db dump, then spinup db restore " + DefaultDumpFile}
	}
	if err := confirmYes(o, what, "Replace the objects in the development database with "+file+"?", "spinup db restore "+file+" --yes"); err != nil {
		return err
	}
	c := composeExec(root, false, "pg_restore", "-U", devUser, "-d", devDatabase, "--clean", "--if-exists")
	if o.DryRun {
		return printCmd(o.Out, c, "< "+quotePath(file))
	}
	f, err := os.Open(file)
	if err != nil {
		return opError(what, err)
	}
	defer func() { _ = f.Close() }()
	c.Stdin, c.Stdout, c.Stderr = f, o.Out, o.ErrOut
	if _, err := runner.Run(ctx, c); err != nil {
		return opError(what, err)
	}
	_, err = fmt.Fprintf(o.Out, "Restored %s\n", file)
	return err
}

// Seed runs every api/db/seeds/*.sql file, in name order, through
// `psql -v ON_ERROR_STOP=1` on the development database.
func Seed(ctx context.Context, o Options, runner run.Runner) error {
	const what = "Cannot seed the database"
	root, config, err := loadProject(o, what)
	if err != nil {
		return err
	}
	if err := guardLocal(o, config, what, "db seed", true); err != nil {
		return err
	}
	files, err := seedFiles(filepath.Join(root, filepath.FromSlash(SeedsDir)))
	if err != nil {
		return opError(what, err)
	}
	if len(files) == 0 {
		_, err := fmt.Fprintf(o.Out, "No seed files in %s.\nTry: add a file such as %s/01_users.sql with INSERT statements\n", SeedsDir, SeedsDir)
		return err
	}
	for _, name := range files {
		rel := SeedsDir + "/" + name
		c := composeExec(root, false, psqlArgs("-v", "ON_ERROR_STOP=1", "-f", "-")...)
		if o.DryRun {
			if err := printCmd(o.Out, c, "< "+quotePath(rel)); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(o.Out, "seed  %s\n", rel); err != nil {
			return err
		}
		if err := runFile(ctx, runner, c, filepath.Join(root, filepath.FromSlash(rel)), o); err != nil {
			return opError(what, err)
		}
	}
	return nil
}

// runFile runs c with the file at path as its stdin.
func runFile(ctx context.Context, runner run.Runner, c run.Cmd, path string, o Options) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	c.Stdin, c.Stdout, c.Stderr = f, o.Out, o.ErrOut
	_, err = runner.Run(ctx, c)
	return err
}

// seedFiles returns the names of the *.sql files in dir, sorted.
func seedFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Reset drops and recreates the public schema of the development database,
// then migrates up through the app binary. It refuses any environment other
// than local development, confirms a protected one, and needs a terminal
// confirmation or Yes.
func Reset(ctx context.Context, o Options, runner run.Runner) error {
	const what = "Cannot reset the database"
	root, config, err := loadProject(o, what)
	if err != nil {
		return err
	}
	if err := guardLocal(o, config, what, "db reset", true); err != nil {
		return err
	}
	if err := confirmYes(o, what, "Drop every table in the development database and migrate up?", "spinup db reset --yes"); err != nil {
		return err
	}
	drop := composeExec(root, false, psqlArgs("-v", "ON_ERROR_STOP=1", "-c", resetSQL)...)
	up := appMigrate(root, "up")
	for _, c := range []run.Cmd{drop, up} {
		if err := runStreaming(ctx, o, runner, what, c); err != nil {
			return err
		}
	}
	return nil
}
