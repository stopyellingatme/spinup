package backup

import (
	"context"
	"fmt"
	"slices"
	"strings"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// restoreRemote observes the commands that can change the live database or begin
// verification. Set flags before Run because SSH can lose a successful reply.
// Unknown backends are treated conservatively by the caller.
type restoreRemote struct {
	remote.Runner
	backend, app           string
	touched, verifyStarted bool
	postgres               string
}

// Run observes restore commands before it delegates them to the VM.
func (r *restoreRemote) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	a := c.Argv
	switch r.backend {
	case "pgbackrest":
		if slices.Contains(a, "--delta") && slices.Contains(a, "restore") {
			r.touched = true
		}
		if len(a) == 3 && a[0] == "docker" && a[1] == "start" && strings.HasPrefix(a[2], r.app+"-verify-") {
			r.verifyStarted = true
		}
	case "pgdump":
		// The staging restore preserves the live database. Only the rename SQL touches it.
		if slices.Contains(a, "psql") {
			for _, arg := range a {
				if strings.HasPrefix(arg, "restored=") {
					r.touched = true
				}
			}
		}
		if len(a) > 5 && a[0] == "sh" && a[3] != "" && strings.Contains(a[3], "pg_restore") {
			r.verifyStarted = true
		}
	}
	result, err := r.Runner.Run(ctx, c)
	if r.backend == "pgbackrest" && len(a) > 0 && a[len(a)-1] == "postgres" {
		if slices.Contains(a, "stop") {
			r.postgres = "STOPPED"
			if err != nil {
				r.postgres = "state unconfirmed after the stop failed"
			}
		}
		if slices.Contains(a, "start") {
			r.postgres = "running"
			if err != nil {
				r.postgres = "state unconfirmed after the start failed"
			}
		}
	}
	return result, err
}

func observeRestore(s *Session) (Backend, *restoreRemote, error) {
	name := s.Config.Backups.Backend
	if name == "" {
		name = "pgbackrest"
	}
	r := &restoreRemote{Runner: s.Remote, backend: name, app: s.App, postgres: "not stopped by this command"}
	env := s.Env
	env.Remote = r
	// Factories bind their runner at construction. Preserve the session's original
	// runner for locks, metrics, and connection ownership.
	backend, err := NewBackend(name, env)
	return backend, r, err
}

func listedTarget(backups []Info, t Target, backend string) (Target, error) {
	for i := len(backups) - 1; i >= 0; i-- {
		b := backups[i]
		if b.Error {
			continue
		}
		if t.Label != "" && b.Label == t.Label {
			return t, nil
		}
		if t.Label == "" && !t.Time.IsZero() && b.Stop.Before(t.Time) {
			return t, nil
		}
		if t.Label == "" && t.Time.IsZero() {
			t.Label = b.Label
			return t, nil
		}
	}
	return t, fmt.Errorf("select %s backup: no available backup matches the target\nTry: select a target from spinup backup list", backend)
}

func shellCommand(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = remote.Quote(arg)
	}
	return strings.Join(parts, " ")
}

func checkRestoreTarget(ctx context.Context, b Backend, name string, t Target) (Target, error) {
	if name == "" {
		name = "pgbackrest"
	}
	if name == "pgdump" && !t.Time.IsZero() {
		return t, fmt.Errorf("select pgdump target: logical dumps do not support point-in-time recovery\nTry: select a dump label from spinup backup list")
	}
	if name == "pgbackrest" && t.Label != "" && !backupLabelPattern.MatchString(t.Label) {
		return t, fmt.Errorf("select pgBackRest backup: invalid label %q\nTry: copy a label from spinup backup list", t.Label)
	}
	backups, err := b.List(ctx)
	if err != nil {
		return t, err
	}
	return listedTarget(backups, t, name)
}
