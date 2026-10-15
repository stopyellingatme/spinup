package backup

import (
	"context"
	"fmt"
	"slices"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// Session holds the project and the connection selected by Open.
type Session struct {
	Env
	owned bool
}

// Close closes an owned SSH connection. Injected runners remain open.
func (s *Session) Close() {
	if s.owned {
		_ = s.Remote.Close(context.Background())
	}
}

// Open loads the project and constructs its configured backup backend.
func Open(o Options) (Backend, *Session, error) {
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("find the spinup project: %w\nTry: run inside a spinup project or pass --dir", err)
	}
	config, err := project.Load(root)
	if err != nil {
		return nil, nil, fmt.Errorf("load the spinup project: %w\nTry: correct spinup.toml", err)
	}
	if o.Env == "" {
		return nil, nil, fmt.Errorf("select backup environment: --env is missing\nTry: pass --env <environment>")
	}
	env, err := config.ResolveEnv(o.Env)
	if err != nil {
		return nil, nil, fmt.Errorf("select backup environment: %w", err)
	}
	if !slices.Contains(config.Modules, "backups") {
		return nil, nil, fmt.Errorf("back up the database: the project does not use the backups module\nTry: add the backups module to the project")
	}
	_, progress := writers(o)
	s := &Session{Env: Env{App: config.App.Name, Environment: env, Config: config, Root: root, Remote: o.Remote, Progress: progress}}
	if s.Remote == nil {
		host, err := remote.HostFromEnv(env)
		if err != nil {
			return nil, nil, err
		}
		runner := o.Runner
		if runner == nil {
			runner = runexec.OSRunner{}
		}
		s.Remote, s.owned = remote.New(host, runner, remote.Options{}), true
	}
	backend, err := NewBackend(config.Backups.Backend, s.Env)
	if err != nil {
		s.Close()
		return nil, nil, err
	}
	return backend, s, nil
}
