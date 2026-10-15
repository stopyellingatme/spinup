package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/db"
	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// MigrationsDir is the migrations directory, relative to the project root.
const MigrationsDir = "api/db/migrations"

// maxMigration is the highest number of a 5-digit goose sequential file.
const maxMigration = 99999

var (
	snakeName     = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	sequentialSQL = regexp.MustCompile(`^([0-9]{5})_(.+)\.sql$`)
	versionedSQL  = regexp.MustCompile(`^([0-9]+)_(.+)\.sql$`)
)

// NextMigrationName returns the goose sequential file name for a new
// migration in dir: "NNNNN_<name>.sql", where NNNNN is the highest 5-digit
// number of the files in dir plus one (ADR 0007 §4). It rejects a name that is
// not snake case, and a name that a migration in dir already has. A missing
// dir gives "00001_<name>.sql".
func NextMigrationName(dir, name string) (string, error) {
	const what = "Cannot name the migration"
	if !snakeName.MatchString(name) {
		return "", &ui.Error{
			What: what,
			Why:  fmt.Sprintf("%q is not snake case", name),
			Try:  "use lower-case words joined by underscores, for example add_widgets",
			Code: ui.ExitUsage,
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", opError(what, err)
	}
	highest := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if m := versionedSQL.FindStringSubmatch(e.Name()); m != nil && m[2] == name {
			return "", &ui.Error{
				What: what,
				Why:  fmt.Sprintf("the migration %s already has the name %s", e.Name(), name),
				Try:  "choose another name, or edit the migration " + e.Name(),
			}
		}
		if m := sequentialSQL.FindStringSubmatch(e.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			highest = max(highest, n)
		}
	}
	if highest >= maxMigration {
		return "", &ui.Error{What: what, Why: "the migration numbers reached 99999", Try: "squash old migrations into one file"}
	}
	return fmt.Sprintf("%05d_%s.sql", highest+1, name), nil
}

// migrationSkeleton is the content of a new migration file.
const migrationSkeleton = `-- +goose Up
-- Write the SQL for this migration here.

-- +goose Down
-- Write the SQL that reverses the Up section here.
`

// New writes a new migration file with a goose Up and Down skeleton in
// api/db/migrations and prints its path, relative to the project root.
func New(o Options, name string) error {
	const what = "Cannot create the migration"
	root, _, err := loadProject(o, what)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, filepath.FromSlash(MigrationsDir))
	file, err := NextMigrationName(dir, name)
	if err != nil {
		return err
	}
	rel := MigrationsDir + "/" + file
	if o.DryRun {
		_, err := fmt.Fprintf(o.Out, "Would create %s\n", rel)
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return opError(what, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, file), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return opError(what, err)
	}
	if _, err := f.WriteString(migrationSkeleton); err != nil {
		_ = f.Close()
		return opError(what, err)
	}
	if err := f.Close(); err != nil {
		return opError(what, err)
	}
	_, err = fmt.Fprintf(o.Out, "Created %s\n", rel)
	return err
}

// Status prints the state of each migration through the app binary. It is
// `spinup db migrate status`.
func Status(ctx context.Context, o Options, runner run.Runner) error {
	return db.Migrate(ctx, migrateOptions(o, "status"), runner)
}

func migrateOptions(o Options, sub string) db.Options {
	return db.Options{Dir: o.Dir, Sub: sub, Env: o.Env, IKnow: o.IKnow, Interactive: o.Interactive, Yes: o.Yes, CI: o.CI, DryRun: o.DryRun, In: o.In, Out: o.Out, ErrOut: o.ErrOut}
}

// Redo rolls back the most recent migration and applies it again, through
// the app binary. It needs IKnow, and a protected environment needs
// confirmation.
func Redo(ctx context.Context, o Options, runner run.Runner) error {
	const what = "Cannot redo the migration"
	if !o.IKnow {
		return &ui.Error{What: "This rolls back the last migration and can lose data", Why: "redo requires explicit acknowledgement", Try: "spinup db redo --i-know"}
	}
	root, config, err := loadProject(o, what)
	if err != nil {
		return err
	}
	if err := guardMigration(o, config, what, "db redo"); err != nil {
		return err
	}
	return runStreaming(ctx, o, runner, what, appMigrate(root, "redo"))
}

// To migrates up or down to version, through the app binary. When the move
// can go down, it needs IKnow, and a protected environment needs
// confirmation. A dry run cannot read the database, so it treats every
// version below the newest file as a move down.
func To(ctx context.Context, o Options, runner run.Runner, version string) error {
	const what = "Cannot migrate to the version"
	target, err := strconv.ParseInt(version, 10, 64)
	if err != nil || target < 0 {
		return &ui.Error{What: what, Why: fmt.Sprintf("%q is not a migration version", version), Try: "spinup db to <version>, for example spinup db to 1", Code: ui.ExitUsage}
	}
	root, config, err := loadProject(o, what)
	if err != nil {
		return err
	}
	newest, err := newestVersion(filepath.Join(root, filepath.FromSlash(MigrationsDir)))
	if err != nil {
		return opError(what, err)
	}
	down := target < newest
	if down && !o.DryRun {
		current, err := currentVersion(ctx, o, runner, root, what)
		if err != nil {
			return err
		}
		down = target < current
	}
	if down {
		if !o.IKnow {
			return &ui.Error{What: "This rolls back migrations and can lose data", Why: "a move down requires explicit acknowledgement", Try: "spinup db to " + version + " --i-know"}
		}
		if err := guardMigration(o, config, what, "db to "+version); err != nil {
			return err
		}
	} else if len(config.Envs) > 0 || o.Env != "" {
		if _, err := config.ResolveEnv(o.Env); err != nil {
			return opError(what, err)
		}
	}
	return runStreaming(ctx, o, runner, what, appMigrate(root, "to", strconv.FormatInt(target, 10)))
}

// currentVersion runs `migrate version` and parses its number.
func currentVersion(ctx context.Context, o Options, runner run.Runner, root, what string) (int64, error) {
	c := appMigrate(root, "version")
	c.Stderr = o.ErrOut
	res, err := runner.Run(ctx, c)
	if err != nil {
		return 0, opError(what, err)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(res.Stdout)), 10, 64)
	if err != nil {
		return 0, &ui.Error{What: what, Why: fmt.Sprintf("%s printed %q, not a version number", c.String(), bytes.TrimSpace(res.Stdout)), Try: "update the kit in api/go.mod, then retry"}
	}
	return v, nil
}

// newestVersion returns the highest version of the migration files in dir.
func newestVersion(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	var newest int64
	for _, e := range entries {
		if m := versionedSQL.FindStringSubmatch(e.Name()); m != nil && !e.IsDir() {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				newest = max(newest, n)
			}
		}
	}
	return newest, nil
}

// runStreaming prints c in a dry run, or runs it with the output streams.
func runStreaming(ctx context.Context, o Options, runner run.Runner, what string, c run.Cmd) error {
	if o.DryRun {
		return printCmd(o.Out, c, "")
	}
	c.Stdout, c.Stderr = o.Out, o.ErrOut
	if _, err := runner.Run(ctx, c); err != nil {
		return opError(what, err)
	}
	return nil
}
