package pg

import (
	"context"
	"slices"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// releaseRunner supplies the active release to backend Compose invocations that
// otherwise depend on the SSH working directory. Shell scripts retain their own paths.
type releaseRunner struct {
	remote.Runner
	u *upgrade
}

func (r releaseRunner) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	if len(c.Argv) >= 4 && slices.Equal(c.Argv[:4], []string{"docker", "compose", "-p", r.u.cfg.App.Name}) && !slices.Contains(c.Argv, "--env-file") {
		args := append([]string{}, c.Argv[:4]...)
		args = append(args, "--env-file", r.u.dir+"/.env")
		if !slices.Contains(c.Argv, "-f") && !slices.Contains(c.Argv, "--file") {
			for _, file := range r.u.files {
				args = append(args, "-f", file)
			}
		}
		c.Argv = append(args, c.Argv[4:]...)
	}
	return r.Runner.Run(ctx, c)
}
