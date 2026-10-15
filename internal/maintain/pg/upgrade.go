package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
	_ "github.com/stopyellingatme/spinup/internal/backup/pgdump"
	"github.com/stopyellingatme/spinup/internal/deploy"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/maintain"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/modules"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// Options selects an upgrade and its safety guards. To is zero for a minor upgrade.
type Options struct {
	Dir, Env                         string
	To                               int
	Major, Cleanup, Plan, Now, IKnow bool
	Confirm                          project.Confirm
	Out, ErrOut                      io.Writer
}

// Deps replaces external resources for offline tests.
type Deps struct {
	Remote  remote.Runner
	Runner  runexec.Runner
	Now     func() time.Time
	HTTPGet func(context.Context, string) (int, []byte, error)
	Sleep   func(context.Context, time.Duration) error
}

type upgrade struct {
	o        Options
	d        Deps
	root     string
	cfg      *project.Config
	env      project.Env
	vm       remote.Runner
	rel      deploy.Release
	dir      string
	files    []string
	live     container
	current  version
	recovery string
}

type container struct {
	ID, Image string
	Config    struct {
		Image, User string
		Env         []string
		Labels      map[string]string
	}
	Mounts []struct{ Type, Name, Destination string }
}

// Run upgrades PostgreSQL or removes the retained volume after a verified major upgrade.
func Run(ctx context.Context, o Options, d Deps) (err error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.ErrOut == nil {
		o.ErrOut = o.Out
	}
	if o.Dir == "" {
		o.Dir = "."
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	u := &upgrade{o: o, d: d}
	defer func() {
		if err != nil && u.recovery != "" {
			fmt.Fprintf(o.ErrOut, "Return to the old state on the VM:\n%s\n", u.recovery)
			err = fmt.Errorf("%w\nTry: return to the old state with %s", err, u.recovery)
		}
	}()
	u.root, err = project.FindRoot(o.Dir)
	if err != nil {
		return failure("find project", err)
	}
	u.cfg, err = project.Load(u.root)
	if err != nil {
		return failure("load project", err)
	}
	if err = u.cfg.Validate(); err != nil {
		return failure("validate project", err)
	}
	if o.Env == "" {
		return failure("select environment", errors.New("--env is required"))
	}
	u.env, err = u.cfg.ResolveEnv(o.Env)
	if err != nil {
		return err
	}
	if o.Cleanup && (!o.Major || o.To != 0) {
		return failure("select cleanup", errors.New("use --cleanup without --to on pg-major-upgrade"))
	}
	if o.Major && !o.Cleanup {
		if _, err = pinnedImage(o.To); err != nil {
			return err
		}
	}
	if o.Plan {
		u.plan()
		return nil
	}
	inside, err := maintain.InWindow(u.cfg.Maintenance.Window, d.Now())
	if err != nil {
		return err
	}
	if !inside && !o.Now {
		return failure("upgrade PostgreSQL", errors.New("outside the maintenance window. Use --now to override"))
	}
	if o.Major {
		if o.Cleanup {
			fmt.Fprintln(o.Out, "Cleanup permanently removes the old database volume and its recovery path.")
		} else {
			fmt.Fprintln(o.Out, "The app will be down from stopping both colors until the smoke check passes.")
		}
		if !o.IKnow {
			return failure("confirm database operation", errors.New("--i-know is required to accept downtime or permanent volume removal"))
		}
	}
	if err = project.ConfirmProtected(u.env, "upgrade PostgreSQL", o.Confirm); err != nil {
		return failure("confirm PostgreSQL operation", err)
	}
	u.vm = d.Remote
	if u.vm == nil {
		host, hostErr := remote.HostFromEnv(u.env)
		if hostErr != nil {
			return hostErr
		}
		if d.Runner == nil {
			d.Runner = runexec.OSRunner{}
		}
		u.vm = remote.New(host, d.Runner, remote.Options{})
		defer func() { _ = u.vm.Close(context.WithoutCancel(ctx)) }()
	}
	fmt.Fprintln(o.Out, "Take the deploy lock")
	lock, err := deploy.AcquireLock(ctx, u.vm, u.cfg.App.Name, deploy.LockOptions{Release: "pg-upgrade", Now: d.Now, Warn: o.ErrOut})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Release(context.WithoutCancel(ctx))) }()
	ctx, cancel := context.WithCancelCause(ctx)
	stop := heartbeat(ctx, cancel, lock)
	defer func() {
		stop()
		if cause := context.Cause(ctx); cause != nil {
			err = errors.Join(err, cause)
		}
		cancel(nil)
	}()
	if o.Cleanup {
		return u.cleanup(ctx)
	}
	if err = u.inspect(ctx); err != nil {
		return err
	}
	if o.Major {
		return u.major(ctx)
	}
	return u.minor(ctx)
}

func failure(step string, err error) error {
	return fmt.Errorf("%s failed: %w\nTry: inspect the PostgreSQL logs and the upgrade recovery instructions", step, err)
}
func (u *upgrade) run(ctx context.Context, args ...string) ([]byte, error) {
	result, err := u.vm.Run(ctx, remote.Cmd{Argv: args, Stderr: u.o.ErrOut, Timeout: 30 * time.Minute})
	if err != nil {
		return nil, failure("run "+strings.Join(args[:min(3, len(args))], " "), err)
	}
	return result.Stdout, nil
}
func (u *upgrade) base(args ...string) []string {
	return deploy.ComposeArgv(u.cfg.App.Name, u.dir, u.files, args...)
}
func (u *upgrade) color(args ...string) []string {
	return deploy.ComposeArgv(u.cfg.App.Name+"-"+u.rel.Color, u.dir, []string{u.dir + "/compose.yml"}, args...)
}
func (u *upgrade) services() []string {
	lock, err := manifest.Load(filepath.Join(u.root, "spinup.lock"))
	if err == nil && modules.WebEnabled(lock) {
		return []string{"api", "web"}
	}
	return []string{"api"}
}
func (u *upgrade) operationDir() string {
	if u.o.Major {
		return "/opt/" + u.cfg.App.Name + "/pg-upgrade"
	}
	return "/opt/" + u.cfg.App.Name + "/pg-minor-upgrade"
}
func (u *upgrade) plan() {
	steps := []string{"Check the maintenance window and protected confirmation", "Take the deploy lock", "Read SHOW server_version and the pinned image"}
	switch {
	case u.o.Cleanup:
		steps = append(steps, "Check the completed upgrade and the active volume", "Check the repository lock answer and external volume mapping", "Remove the retained old volume with confirmation")
	case u.o.Major:
		steps = append(steps, "Save the old image, Compose files, and recovery command", "Stop api and web of both colors (the app is down)", "Take a full backup with pgdump", "Start the target pinned image on a new volume", "Restore the dump and run ANALYZE", "Compare goose_db_version and every user table row count", "Stop the old postgres and swap the Compose image and volume", "Upgrade the pgBackRest stanza when the backups module uses pgbackrest", "Run migrate", "Start the live color", "Run the deploy smoke checks", "Retain the old volume until --cleanup")
	default:
		steps = append(steps, "Exit successfully if the pinned version is already running", "Take a backup with the configured backend", "Save the old image and recovery command", "Build postgres from the release Dockerfile.postgres", "Recreate only postgres (restart downtime)", "Wait for health and print SHOW server_version")
	}
	for _, step := range steps {
		fmt.Fprintln(u.o.Out, "Plan: "+step)
	}
	if u.o.Major && !u.o.Cleanup {
		u.repositoryChange()
	}
}

func (u *upgrade) inspect(ctx context.Context) error {
	fmt.Fprintln(u.o.Out, "Read the live release and PostgreSQL version")
	data, err := u.run(ctx, "cat", "/opt/"+u.cfg.App.Name+"/current/release.json")
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &u.rel); err != nil {
		return failure("decode live release", err)
	}
	if !deploy.ValidReleaseID(u.rel.Release) || (u.rel.Color != deploy.Blue && u.rel.Color != deploy.Green) || len(u.rel.GitSHA) < 12 {
		return failure("read live release", errors.New("invalid release, color, or git SHA"))
	}
	u.dir = "/opt/" + u.cfg.App.Name + "/releases/" + u.rel.Release
	data, err = u.run(ctx, "docker", "ps", "-q", "--filter", "label=com.docker.compose.project="+u.cfg.App.Name, "--filter", "label=com.docker.compose.service=postgres")
	if err != nil {
		return err
	}
	ids := strings.Fields(string(data))
	if len(ids) != 1 {
		return failure("find postgres", errors.New("expected one running postgres container"))
	}
	data, err = u.run(ctx, "docker", "inspect", "--format", "{{json .}}", ids[0])
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &u.live); err != nil {
		return failure("inspect postgres", err)
	}
	u.live.ID = ids[0]
	if u.live.Image == "" || u.live.Config.Image == "" || u.live.Config.User == "" {
		return failure("inspect postgres", errors.New("image or runtime user is missing"))
	}
	files := u.live.Config.Labels["com.docker.compose.project.config_files"]
	if files == "" {
		return failure("inspect postgres", errors.New("the Compose config_files label is missing"))
	}
	for _, file := range strings.Split(files, ",") {
		if filepath.Dir(file) != u.dir {
			return failure("inspect postgres", errors.New("a Compose file is outside the current release"))
		}
		u.files = append(u.files, file)
	}
	if !slices.Contains(u.files, u.dir+"/compose.edge.yml") {
		return failure("inspect postgres", errors.New("compose.edge.yml is missing from the active configuration"))
	}
	data, err = u.sql(ctx, u.live.ID, "SHOW server_version;\n")
	if err != nil {
		return err
	}
	u.current, err = parseVersion(strings.TrimSpace(string(data)))
	return err
}

func (u *upgrade) sql(ctx context.Context, id, script string) ([]byte, error) {
	args := []string{"docker", "exec", "-i", id, "sh", "-ec", `exec psql -XAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f -`}
	result, err := u.vm.Run(ctx, remote.Cmd{Argv: args, Stdin: strings.NewReader(script), Stderr: u.o.ErrOut, Timeout: 30 * time.Minute})
	if err != nil {
		return nil, failure("query postgres", err)
	}
	return result.Stdout, nil
}
func (u *upgrade) backup(ctx context.Context, name string) (backup.Result, error) {
	if name == "" {
		name = u.cfg.Backups.Backend
	}
	b, err := backup.NewBackend(name, backup.Env{App: u.cfg.App.Name, Environment: u.env, Config: u.cfg, Root: u.root, Remote: releaseRunner{Runner: u.vm, u: u}, Progress: u.o.ErrOut})
	if err != nil {
		return backup.Result{}, err
	}
	result, err := b.Backup(ctx, backup.Full)
	if err != nil {
		return result, err
	}
	fmt.Fprintf(u.o.Out, "Safety backup: %s (%s)\n", result.Label, name)
	if err = backup.RecordMetrics(ctx, u.vm, u.cfg.App.Name, backup.Sample{Kind: backup.Full, Success: result.Stop, Size: result.Size}); err != nil {
		return result, err
	}
	return result, nil
}

func heartbeat(ctx context.Context, cancel context.CancelCauseFunc, lock *deploy.Lock) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := lock.Refresh(ctx); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	return func() { close(done); <-stopped }
}
