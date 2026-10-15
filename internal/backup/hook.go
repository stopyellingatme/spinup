package backup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// HookName is the name of the pre-deploy backup hook.
const HookName = "backup"

// SafeMarker marks a migration that keeps all data. `deploy --skip-backup` is allowed only
// when every pending migration has this line.
const SafeMarker = "-- spinup:safe"

// MigrationsDir is the directory of the goose migrations in a project.
const MigrationsDir = "api/db/migrations"

func init() { deploy.RegisterPreDeploy(deploy.Hook{Name: HookName, Run: Hook}) }

// DeployOptions carries the deploy flags that the backup hook reads.
// deploy.HookContext has no field for them, so the deploy command puts them in the context.
type DeployOptions struct {
	// Dir is the project directory. Empty means the current directory.
	Dir string
	// SkipBackup is the --skip-backup flag of spinup deploy.
	SkipBackup bool
}

type deployOptionsKey struct{}

// WithDeployOptions returns a context that carries o to the pre-deploy backup hook.
// spinup deploy calls it with its --dir and --skip-backup values before it runs the hooks.
func WithDeployOptions(ctx context.Context, o DeployOptions) context.Context {
	return context.WithValue(ctx, deployOptionsKey{}, o)
}

// DeployOptionsFrom returns the options that WithDeployOptions stored in ctx.
func DeployOptionsFrom(ctx context.Context) DeployOptions {
	o, _ := ctx.Value(deployOptionsKey{}).(DeployOptions)
	return o
}

// Hook is the pre-deploy hook: it takes a diff backup before every deploy.
//
//   - A project without the backups module skips it.
//   - A VM without a recorded release and a running postgres container skips it.
//   - --skip-backup skips it, unless a pending migration lacks the -- spinup:safe marker.
func Hook(ctx context.Context, c *deploy.HookContext) error {
	if c == nil {
		return errors.New("pre-deploy backup: the hook context is missing\nTry: report this spinup bug")
	}
	out := c.Out
	if out == nil {
		out = io.Discard
	}
	o := DeployOptionsFrom(ctx)
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return fmt.Errorf("pre-deploy backup: %w", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ConfigPath))); errors.Is(err, fs.ErrNotExist) {
		_, _ = fmt.Fprintln(out, "backup: skipped, the project does not use the backups module")
		return nil
	}
	data, err := ReadConfig(root)
	if err != nil {
		return err
	}
	config, err := project.Load(root)
	if err != nil {
		return fmt.Errorf("pre-deploy backup: %w", err)
	}
	retention, err := Retention(config)
	if err != nil {
		return err
	}
	app := c.App
	if app == "" {
		app = config.App.Name
	}
	releases, err := deploy.ReadReleases(ctx, c.Remote, app)
	if err != nil {
		return fmt.Errorf("pre-deploy backup: determine whether this is the first deploy: %w", err)
	}
	running, err := postgresRunning(ctx, c.Remote, app)
	if err != nil {
		return err
	}
	if !running {
		if len(releases) == 0 {
			_, _ = fmt.Fprintln(out, "backup: skipped, the database does not run yet (first deploy)")
			return nil
		}
		if o.SkipBackup {
			unsafe, err := unsafePendingAfter(filepath.Join(root, filepath.FromSlash(MigrationsDir)), releases[0].MigrationVersion)
			if err != nil {
				return fmt.Errorf("refuse --skip-backup: the pending migrations could not be checked: %w\nTry: start postgres, then deploy again", err)
			}
			if len(unsafe) > 0 {
				return fmt.Errorf("refuse --skip-backup: pending migrations lack the %q marker: %s\nTry: start postgres, or add a %q line to every pending migration that keeps all data", SafeMarker, strings.Join(unsafe, ", "), SafeMarker)
			}
			_, _ = fmt.Fprintln(out, "backup: skipped (--skip-backup), every pending migration is marked safe")
			return nil
		}
		return errors.New("pre-deploy backup: postgres is not running on an existing deployment\nTry: start postgres, or pass --skip-backup when every pending migration is marked safe")
	}
	if o.SkipBackup {
		unsafe, err := unsafePending(ctx, c.Remote, app, filepath.Join(root, filepath.FromSlash(MigrationsDir)))
		if err != nil {
			return fmt.Errorf("refuse --skip-backup: the pending migrations could not be checked: %w\nTry: deploy without --skip-backup", err)
		}
		if len(unsafe) > 0 {
			return fmt.Errorf("refuse --skip-backup: pending migrations lack the %q marker: %s\nTry: deploy without --skip-backup, or add a %q line to a migration that keeps all data", SafeMarker, strings.Join(unsafe, ", "), SafeMarker)
		}
		_, _ = fmt.Fprintln(out, "backup: skipped (--skip-backup), every pending migration is marked safe")
		return nil
	}
	// Record whether every pending migration is marked safe (rollback --with-db-plan uses it).
	if unsafe, err := unsafePending(ctx, c.Remote, app, filepath.Join(root, filepath.FromSlash(MigrationsDir))); err == nil && c.Results != nil {
		c.Results[deploy.ResultMigrationsSafe] = strconv.FormatBool(len(unsafe) == 0)
	}
	backend := &PGBackRest{App: app, Remote: c.Remote, Config: data, RetentionFull: retention, Progress: out}
	_, _ = fmt.Fprintf(out, "backup: taking a %s backup before release %s\n", Diff, c.Release)
	started := time.Now().UTC()
	result, err := backend.Backup(ctx, Diff)
	if err != nil {
		return fmt.Errorf("pre-deploy backup: %w", err)
	}
	backupMetrics(ctx, c.Remote, app, result, out)
	if c.Results != nil {
		// A point-in-time restore to just before the backup started returns the database as
		// it was before this deploy's migrations.
		c.Results[deploy.ResultBackupTime] = started.Format(time.RFC3339)
	}
	_, _ = fmt.Fprintf(out, "backup: %s (%s), size %s, took %s\n", result.Label, result.Kind, HumanSize(result.Size), result.Duration)
	return nil
}

// postgresRunning reports whether the postgres container of the base project runs.
func postgresRunning(ctx context.Context, r remote.Runner, app string) (bool, error) {
	if r == nil {
		return false, errors.New("pre-deploy backup: the remote runner is missing\nTry: report this spinup bug")
	}
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"docker", "compose", "-p", app, "ps", "--status", "running", "-q", "postgres"}})
	if err != nil {
		return false, fmt.Errorf("pre-deploy backup: list the running postgres container: %w\nTry: check Docker on the VM", err)
	}
	return strings.TrimSpace(string(result.Stdout)) != "", nil
}

// The queries of AppliedVersion. A missing goose table gives -1.
const (
	gooseTableQuery   = "SELECT CASE WHEN to_regclass('public.goose_db_version') IS NULL THEN '-1' ELSE '0' END"
	gooseVersionQuery = "SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied"
)

// AppliedVersion returns the newest applied goose migration version on the VM.
// A database without the goose table gives zero.
func AppliedVersion(ctx context.Context, r remote.Runner, app string) (int64, error) {
	psql := append(composeExec(app), "psql", "-U", "app", "-d", "app", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-c")
	result, err := r.Run(ctx, remote.Cmd{Argv: append(append([]string(nil), psql...), gooseTableQuery)})
	if err != nil {
		return 0, fmt.Errorf("query the goose table: %w", err)
	}
	if strings.TrimSpace(string(result.Stdout)) == "-1" {
		return 0, nil
	}
	result, err = r.Run(ctx, remote.Cmd{Argv: append(append([]string(nil), psql...), gooseVersionQuery)})
	if err != nil {
		return 0, fmt.Errorf("query the applied migration version: %w", err)
	}
	version, err := strconv.ParseInt(strings.TrimSpace(string(result.Stdout)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse the applied migration version %q: %w", strings.TrimSpace(string(result.Stdout)), err)
	}
	return version, nil
}

func unsafePending(ctx context.Context, r remote.Runner, app, dir string) ([]string, error) {
	applied, err := AppliedVersion(ctx, r, app)
	if err != nil {
		return nil, err
	}
	return UnsafePending(dir, applied)
}

func unsafePendingAfter(dir, version string) ([]string, error) {
	applied, err := strconv.ParseInt(version, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse the recorded migration version %q: %w", version, err)
	}
	return UnsafePending(dir, applied)
}

// UnsafePending returns the migration files in dir with a version above applied that lack
// the SafeMarker line, in version order.
func UnsafePending(dir string, applied int64) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations directory %s: %w", dir, err)
	}
	type migration struct {
		version int64
		name    string
	}
	var pending []migration
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.ParseInt(prefix, 10, 64)
		if !ok || err != nil {
			continue
		}
		if version <= applied {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		if !HasSafeMarker(data) {
			pending = append(pending, migration{version, name})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].version < pending[j].version })
	names := make([]string, len(pending))
	for i, m := range pending {
		names[i] = m.name
	}
	return names, nil
}

// HasSafeMarker reports whether a migration has a line that starts with SafeMarker.
func HasSafeMarker(sql []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(sql))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == SafeMarker || strings.HasPrefix(line, SafeMarker+" ") || strings.HasPrefix(line, SafeMarker+"\t") {
			return true
		}
	}
	return false
}
