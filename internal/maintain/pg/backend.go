package pg

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/backup"
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

func (u *upgrade) usesPGBackRest() bool {
	return slices.Contains(u.cfg.Modules, "backups") && (u.cfg.Backups.Backend == "" || u.cfg.Backups.Backend == "pgbackrest")
}

// majorBackupConfig preserves repository options and updates the app's data path.
func (u *upgrade) majorBackupConfig() ([]byte, error) {
	if !u.usesPGBackRest() {
		return nil, nil
	}
	data, err := backup.ReadConfig(u.root)
	if err != nil {
		return nil, err
	}
	retention, err := backup.Retention(u.cfg)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	stanza, found := "", false
	for i, line := range lines {
		text := strings.TrimSpace(line)
		if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
			stanza = text
		}
		key, _, ok := strings.Cut(text, "=")
		if stanza == "["+u.cfg.App.Name+"]" && ok && strings.TrimSpace(key) == "pg1-path" {
			path := "/var/lib/postgresql/data/pgdata"
			if u.o.To == 18 {
				path = "/var/lib/postgresql/18/docker"
			}
			lines[i] = "pg1-path=" + path
			found = true
		}
	}
	if !found {
		return nil, failure("prepare pgBackRest configuration", errors.New("the app stanza has no pg1-path in "+backup.ConfigPath))
	}
	return backup.WithRetention([]byte(strings.Join(lines, "\n")), retention), nil
}
