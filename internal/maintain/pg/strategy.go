package pg

import (
	"context"
	"io"
	"io/fs"
	"strings"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

// Strategy supplies an optional major upgrade workflow under the shared safety guards.
type Strategy interface {
	// Plan prints the steps without remote access.
	Plan(io.Writer, bool)
	// Execute performs the upgrade under the deploy lock.
	Execute(context.Context, *Session) error
	// Cleanup removes retained data after verification.
	Cleanup(context.Context, *Session) error
}

// RunStrategy runs a major strategy with the existing window, confirmation, lock, and heartbeat.
func RunStrategy(ctx context.Context, o Options, d Deps, strategy Strategy) error {
	o.Major = true
	return run(ctx, o, d, strategy)
}

// Session provides the shared operations of a locked PostgreSQL upgrade.
// Its methods must only be used during a Strategy call.
type Session struct{ u *upgrade }

// Details describes the inspected release and database.
type Details struct {
	// Root is the local project root.
	Root string
	// Dir is the active release directory on the VM.
	Dir string
	// OperationDir holds the saved recovery files on the VM.
	OperationDir string
	// ID identifies the inspected PostgreSQL container.
	ID string
	// Volume names the live data volume.
	Volume string
	// Mount is the destination of Volume in the live container.
	Mount string
	// Data is the live PGDATA path.
	Data string
	// User is the runtime UID:GID.
	User string
	// Current is the live PostgreSQL major.
	Current int
	// To is the target PostgreSQL major.
	To int
	// Config is the loaded project configuration.
	Config *project.Config
	// Env is the resolved deployment environment.
	Env project.Env
	// Out receives progress messages.
	Out io.Writer
}

// Details returns the live database layout and the selected operation.
func (s *Session) Details() Details {
	u := s.u
	d := Details{Root: u.root, Dir: u.dir, OperationDir: u.operationDir(), ID: u.live.ID, Volume: u.volume(), User: u.live.Config.User, Current: u.current.major, To: u.o.To, Config: u.cfg, Env: u.env, Out: u.o.Out}
	for _, m := range u.live.Mounts {
		if m.Type == "volume" && m.Name == d.Volume {
			d.Mount = m.Destination
		}
	}
	for _, e := range u.live.Config.Env {
		if v, ok := strings.CutPrefix(e, "PGDATA="); ok {
			d.Data = v
		}
	}
	return d
}

// Inspect reads the active release, image, volume, and server version.
func (s *Session) Inspect(ctx context.Context) error { return s.u.inspect(ctx) }

// Prepare validates the target, backup configuration, and smoke configuration before downtime.
func (s *Session) Prepare() (stackversions.Image, error) {
	u := s.u
	image, err := targetImage(u.current.major, u.o.To)
	if err != nil {
		return image, err
	}
	if _, err = u.majorBackupConfig(); err != nil {
		return image, err
	}
	if _, err = deploy.LoadEnvFile(u.root, u.env.Name); err != nil {
		return image, err
	}
	_, err = u.publicOrigin()
	return image, err
}

// Run executes a remote command through the shared runner and error formatter.
func (s *Session) Run(ctx context.Context, args ...string) ([]byte, error) {
	return s.u.run(ctx, args...)
}

// Upload writes a remote recovery or helper file.
func (s *Session) Upload(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	if err := s.u.vm.Upload(ctx, data, path, mode); err != nil {
		return failure("write "+path, err)
	}
	return nil
}

// Base returns Compose arguments for the active base project.
func (s *Session) Base(args ...string) []string { return s.u.base(args...) }

// StopColors stops both app colors using the shared maintenance operation.
func (s *Session) StopColors(ctx context.Context) error {
	_, err := s.u.run(ctx, "sh", "-ec", stopColorsScript(s.u.cfg.App.Name, s.u.appServices()...))
	return err
}

// Backup takes a full backup with the configured backend and records its metrics.
func (s *Session) Backup(ctx context.Context) (backup.Result, error) { return s.u.backup(ctx, "") }

// SaveRecovery retains the old image and files, but refuses unsafe old-cluster recovery.
// The refusal must contain the exact backup restore command.
func (s *Session) SaveRecovery(ctx context.Context, refusal string) error {
	s.u.rollbackRefusal = refusal
	return s.u.saveRecovery(ctx, s.u.volume())
}

// Switch builds and starts the target server, then upgrades the pgBackRest stanza when configured.
func (s *Session) Switch(ctx context.Context, image stackversions.Image, volume, dataPath string) error {
	return s.u.switchMajor(ctx, image, volume, dataPath, true)
}

// Finish migrates the release, starts the live color, and runs the shared smoke checks.
func (s *Session) Finish(ctx context.Context) error { return s.u.finish(ctx) }

// RepositoryChange prints the existing manual repository steps with the retained volume name.
func (s *Session) RepositoryChange(volume string) { s.u.repositoryChangeAt(volume) }

// Query runs a read-only cluster inspection before the source server stops.
func (s *Session) Query(ctx context.Context, query string) ([]byte, error) {
	return s.u.sql(ctx, s.u.live.ID, query)
}

// DatabaseUser returns the configured PostgreSQL administrator.
func (s *Session) DatabaseUser() string {
	for _, e := range s.u.live.Config.Env {
		if v, ok := strings.CutPrefix(e, "POSTGRES_USER="); ok {
			return v
		}
	}
	return ""
}
