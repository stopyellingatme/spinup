package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/remote"
)

var backupLabelPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}F(_[0-9]{8}-[0-9]{6}[DI])?$`)

// ParseTarget validates the mutually exclusive label and RFC 3339 time flags.
func ParseTarget(label, at string) (Target, error) {
	if label != "" && at != "" {
		return Target{}, fmt.Errorf("select backup target: --label and --time are mutually exclusive\nTry: pass only one target flag")
	}
	t := Target{Label: label}
	if at != "" {
		var err error
		t.Time, err = time.Parse(time.RFC3339, at)
		if err != nil {
			return Target{}, fmt.Errorf("parse restore time: %w\nTry: pass --time in RFC 3339 format", err)
		}
	}
	return t, nil
}

func validateTarget(t Target) error {
	if t.Label != "" && !t.Time.IsZero() {
		return fmt.Errorf("select backup target: label and time are mutually exclusive\nTry: select one target")
	}
	return nil
}

func (p *PGBackRest) resolveTarget(ctx context.Context, t Target) (Target, error) {
	if err := validateTarget(t); err != nil {
		return t, err
	}
	if t.Label != "" && !backupLabelPattern.MatchString(t.Label) {
		return t, fmt.Errorf("select pgBackRest backup: invalid label %q\nTry: copy a label from spinup backup list", t.Label)
	}
	bs, err := p.List(ctx)
	if err != nil {
		return t, err
	}
	return listedTarget(bs, t, "pgBackRest")
}

func restoreArgs(app string, t Target) []string {
	args := []string{"--stanza=" + app}
	if t.Label != "" {
		args = append(args, "--set="+t.Label)
	}
	if !t.Time.IsZero() {
		args = append(args, "--type=time", "--target="+t.Time.UTC().Format(time.RFC3339Nano), "--target-action=promote")
	} else {
		args = append(args, "--type=immediate", "--target-action=promote")
	}
	return args
}

func baseCompose(app string, args ...string) []string {
	dir := "/opt/" + app + "/current"
	return deploy.ComposeArgv(app, dir, []string{dir + "/compose.edge.yml", dir + "/compose.backups.yml"}, args...)
}

func (p *PGBackRest) remoteCommand(ctx context.Context, args ...string) ([]byte, error) {
	if p.Remote == nil {
		return nil, fmt.Errorf("run backup command: the remote runner is missing\nTry: report this spinup bug")
	}
	result, err := p.Remote.Run(ctx, remote.Cmd{Argv: args, Stderr: p.Progress, Timeout: 30 * time.Minute})
	if err != nil {
		return nil, fmt.Errorf("run backup command on the VM: %w\nTry: check spinup backup status --env <env> and the server logs", err)
	}
	return result.Stdout, nil
}

// Restore restores into a separate container or the stopped live volume.
func (p *PGBackRest) Restore(ctx context.Context, t Target, into Into) (err error) {
	if into.New == into.InPlace {
		return fmt.Errorf("select restore destination: choose exactly one destination\nTry: pass --into new or --into in-place")
	}
	t, err = p.resolveTarget(ctx, t)
	if err != nil {
		return err
	}
	if into.New {
		_, err = p.restoreContainer(ctx, t, false)
		return err
	}
	stopping, touched := false, false
	defer func() {
		if err == nil || !stopping {
			return
		}
		if !touched {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_, restartErr := p.remoteCommand(cleanup, baseCompose(p.App, "start", "--wait", "postgres")...)
			err = errors.Join(err, restartErr)
			return
		}
		err = fmt.Errorf("%w\npostgres is STOPPED or its state is unconfirmed. The database may be partially restored.\nTry: inspect the database before starting postgres with: %s", err, shellCommand(baseCompose(p.App, "start", "--wait", "postgres")))
	}()
	p.restoreProgress("Stopping postgres")
	stopping = true
	if _, err = p.remoteCommand(ctx, baseCompose(p.App, "stop", "postgres")...); err != nil {
		return err
	}
	p.restoreProgress("Restoring postgres")
	args := baseCompose(p.App, "run", "--rm", "--no-deps", "-T", "--entrypoint", "sh", "postgres", "-c", SecretLoader+`exec pgbackrest "$@"`, "pgbackrest")
	args = append(args, restoreArgs(p.App, t)...)
	args = append(args, "--delta", "restore")
	touched = true // A lost SSH reply can hide a partial restore.
	if _, err = p.remoteCommand(ctx, args...); err != nil {
		return err
	}
	p.restoreProgress("Starting postgres")
	_, err = p.remoteCommand(ctx, baseCompose(p.App, "start", "--wait", "postgres")...)
	return err
}

type postgresContainer struct {
	Config struct {
		Image, User string
		Env         []string
	}
	Mounts []struct{ Destination string }
}

func (p *PGBackRest) restoreContainer(ctx context.Context, t Target, verify bool) (result VerifyResult, err error) {
	start := time.Now()
	defer func() {
		if result.RTO == 0 {
			result.RTO = time.Since(start)
		}
		if err != nil {
			result.OK = false
		}
	}()
	label := t.Label
	if label == "" {
		label = t.Time.UTC().Format("20060102T150405Z")
	}
	result.Label = label
	purpose := "restore"
	if verify {
		purpose = "verify"
	}
	name := p.App + "-" + purpose + "-" + label
	ids, err := p.remoteCommand(ctx, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+p.App, "--filter", "label=com.docker.compose.service=postgres")
	if err != nil {
		return result, err
	}
	id := strings.Fields(string(ids))
	if len(id) != 1 {
		return result, fmt.Errorf("inspect postgres: expected one container, found %d\nTry: deploy the postgres service before restoring", len(id))
	}
	data, err := p.remoteCommand(ctx, "docker", "inspect", "--format", "{{json .}}", id[0])
	if err != nil {
		return result, err
	}
	var live postgresContainer
	if err = json.Unmarshal(data, &live); err != nil {
		return result, fmt.Errorf("inspect postgres: %w\nTry: check the postgres container on the VM", err)
	}
	pgdata := ""
	for _, v := range live.Config.Env {
		if value, ok := strings.CutPrefix(v, "PGDATA="); ok {
			pgdata = value
		}
	}
	mount := ""
	for _, m := range live.Mounts {
		if (m.Destination == "/var/lib/postgresql" || m.Destination == "/var/lib/postgresql/data") && strings.HasPrefix(pgdata, m.Destination+"/") {
			mount = m.Destination
		}
	}
	if live.Config.Image == "" || live.Config.User == "" || mount == "" {
		return result, fmt.Errorf("inspect postgres: image, user, or PGDATA mount is missing\nTry: restore the generated postgres service configuration")
	}
	root := "/opt/" + p.App
	args := []string{"docker", "create", "--name", name, "--network", p.App + "-backup", "--env", "PGDATA=" + pgdata, "--env", "PGBACKREST_CONFIG=/var/lib/pgbackrest/pgbackrest.conf", "--mount", "type=volume,dst=" + mount, "--mount", "type=bind,src=" + root + "/backups,dst=/var/lib/pgbackrest,readonly", "--mount", "type=bind,src=" + root + "/secrets,dst=/run/secrets,readonly", "--mount", "type=bind,src=" + root + "/postgres,dst=/etc/postgresql,readonly"}
	if !verify {
		args = append(args, "--publish", "127.0.0.1:5433:5432")
	}
	// The anonymous volume starts as root. Drop to the live service's user after chown.
	script := `set -eu; mkdir -p "$PGDATA"; chown -R "$1" "$2"; shift 2; exec gosu "$@"`
	recovery := SecretLoader + `set -e; pgbackrest "$@" restore; exec postgres -c config_file=/etc/postgresql/postgresql.conf -c archive_mode=off -c ` + remote.Quote("restore_command="+SecretLoader+"exec pgbackrest --archive-async=n --stanza="+p.App+" archive-get %f %p")
	args = append(args, "--entrypoint", "sh", live.Config.Image, "-c", script, "spinup-restore", live.Config.User, mount, live.Config.User, "sh", "-c", recovery, "pgbackrest")
	args = append(args, restoreArgs(p.App, t)...)
	if _, err = p.remoteCommand(ctx, args...); err != nil {
		return result, err
	}
	defer func() {
		if verify || err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_, cleanupErr := p.remoteCommand(cleanup, "docker", "rm", "-fv", name)
			err = errors.Join(err, cleanupErr)
		}
	}()
	if _, err = p.remoteCommand(ctx, "docker", "start", name); err != nil {
		return result, err
	}
	// Wait for promotion as well as readiness. A standby can answer pg_isready.
	wait := `set -eu; i=0; until pg_isready -U app -d app >/dev/null 2>&1 && [ "$(psql -U app -d app -XAt -v ON_ERROR_STOP=1 -c 'SELECT pg_is_in_recovery()')" = f ]; do i=$((i+1)); [ "$i" -lt 120 ] || exit 1; sleep 1; done; exec psql -U app -d app -XAt -v ON_ERROR_STOP=1 -c 'SELECT count(*) FROM goose_db_version'`
	if _, err = p.remoteCommand(ctx, "docker", "exec", name, "sh", "-c", wait); err != nil {
		return result, err
	}
	result.RTO = time.Since(start)
	result.OK = true
	return result, nil
}

func (p *PGBackRest) restoreProgress(step string) {
	if p.Progress != nil {
		fmt.Fprintln(p.Progress, step)
	}
}
