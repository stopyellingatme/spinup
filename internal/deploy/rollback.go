package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// RollbackOptions selects a stored release without changing the database.
type RollbackOptions struct {
	Options
	// To selects a release ID. Empty selects the previous release.
	To string
	// WithDBPlan prints manual point-in-time recovery instructions.
	WithDBPlan bool
}

// rollbackSnapshot accepts recovery metadata from release.json without changing Release.
// Older deployers omit this metadata. Missing safety metadata is treated conservatively.
type rollbackSnapshot struct {
	Release
	MigrationsSafe      *bool     `json:"migrations_safe,omitempty"`
	PreDeployBackupTime time.Time `json:"pre_deploy_backup_time,omitzero"`
}

func readReleaseSnapshot(ctx context.Context, vm remote.Runner, app, id string) (*rollbackSnapshot, error) {
	if !ValidReleaseID(id) {
		return nil, errors.New("invalid release ID\nTry: select an ID from spinup releases list")
	}
	p := "/opt/" + app + "/releases/" + id + "/release.json"
	result, err := vm.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", readRemoteFileScript, "spinup-read", p}})
	if err != nil {
		return nil, fmt.Errorf("read release snapshot: %w\nTry: check the SSH connection", err)
	}
	var snap rollbackSnapshot
	if err := json.Unmarshal(result.Stdout, &snap); err != nil {
		return nil, fmt.Errorf("decode release snapshot: %w\nTry: inspect release.json on the VM", err)
	}
	if snap.Release.Release != id || (snap.App != "" && snap.App != app) {
		return nil, errors.New("release snapshot does not match its directory or app\nTry: inspect release.json on the VM")
	}
	return &snap, nil
}

func rollbackTarget(entries []ReleaseEntry, current, to string) (string, error) {
	if to != "" && to != current && slices.ContainsFunc(entries, func(e ReleaseEntry) bool { return e.Release == to }) {
		return to, nil
	}
	if to == "" {
		for i, e := range entries {
			if e.Release == current && i+1 < len(entries) {
				return entries[i+1].Release, nil
			}
		}
	}
	return "", errors.New("cannot select a rollback release: no matching previous release\nTry: run spinup releases list and pass --to <release-id>")
}

// Rollback switches to a stored release, tests it, and moves current under the deploy lock.
// It never runs migrations or restores the database.
func Rollback(ctx context.Context, o RollbackOptions, d Deps) (err error) {
	o.fill()
	d.fill()
	if o.To != "" && !ValidReleaseID(o.To) {
		return errors.New("invalid rollback release ID\nTry: select an ID from spinup releases list")
	}
	app, env, err := target(o.Options)
	if err != nil {
		return err
	}
	if err := project.ConfirmProtected(env, "rollback", o.Confirm); err != nil {
		return err
	}
	vm, err := dial(ctx, o.Options, d, env)
	if err != nil {
		return err
	}
	defer func() { _ = vm.Close(context.WithoutCancel(ctx)) }()
	lock, err := AcquireLock(ctx, vm, app, LockOptions{Owner: d.User(), Release: "rollback", TTL: o.LockTTL, Now: d.Now, Warn: o.ErrOut})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Release(context.WithoutCancel(ctx))) }()
	journal := &Journal{Remote: vm, App: app, Release: "rollback", Now: d.Now}
	refreshed := d.Now()
	step := func(name string, run func() error) error {
		if d.Now().Sub(refreshed) >= o.LockTTL/3 {
			if err := lock.Refresh(ctx); err != nil {
				return err
			}
			refreshed = d.Now()
		}
		name = "rollback-" + name
		if err := journal.Start(ctx, name); err != nil {
			return err
		}
		if err := run(); err != nil {
			return errors.Join(fmt.Errorf("rollback failed at %s: %w\nTry: inspect spinup deploy status before retrying rollback", name, err), journal.Fail(context.WithoutCancel(ctx), name, err))
		}
		return journal.Done(ctx, name, "")
	}
	var entries []ReleaseEntry
	var current *Release
	var snap, left *rollbackSnapshot
	if err := step("select", func() error {
		var err error
		entries, err = ReadReleases(ctx, vm, app)
		if err != nil {
			return err
		}
		current, err = readRelease(ctx, vm, app, "")
		if err != nil {
			return err
		}
		if current == nil {
			return errors.New("there is no current release")
		}
		id, err := rollbackTarget(entries, current.Release, o.To)
		if err != nil {
			return err
		}
		snap, err = readReleaseSnapshot(ctx, vm, app, id)
		if err != nil {
			return err
		}
		left, err = readReleaseSnapshot(ctx, vm, app, current.Release)
		if err != nil {
			return err
		}
		if len(snap.GitSHA) < 12 || !ValidReleaseID(snap.Release.Release) || (snap.Color != Blue && snap.Color != Green) {
			return errors.New("the target snapshot has invalid build metadata")
		}
		return nil
	}); err != nil {
		return err
	}
	if o.WithDBPlan || (left.MigrationsSafe != nil && !*left.MigrationsSafe) || (left.MigrationsSafe == nil && left.MigrationVersion != snap.MigrationVersion) {
		writeDBPlan(o, app, left)
	}
	dir := "/opt/" + app + "/releases/" + snap.Release.Release
	var color string
	if err := step("configure", func() error {
		upstream, err := ReadUpstream(ctx, vm, app)
		if err != nil {
			return err
		}
		color = OtherColor(LiveColor(upstream))
		// Keep all stored settings and image references. Only the runtime color changes.
		_, err = vm.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", rollbackColorScript, "spinup-rollback", dir, color, app}})
		return err
	}); err != nil {
		return err
	}
	web := slices.ContainsFunc(snap.Images, func(i Image) bool { return i.Service == "web" })
	if err := step("switch", func() error {
		// The record of the target release gives its sidecar services and its Compose files.
		switchErr := Switch(ctx, vm, SwitchOptions{App: app, ReleaseDir: dir, Color: color, Services: ColorServices(web, snap.Services), ComposeFiles: ColorComposeFiles(snap.Services), HealthTimeout: o.HealthTimeout, Out: o.Out, Now: d.Now, Sleep: d.Sleep})
		if switchErr != nil {
			cleanup := context.WithoutCancel(ctx)
			upstream, readErr := ReadUpstream(cleanup, vm, app)
			if readErr != nil {
				return errors.Join(switchErr, readErr)
			}
			if LiveColor(upstream) != color {
				_, restoreErr := vm.Run(cleanup, remote.Cmd{Argv: []string{"sh", "-c", rollbackRestoreConfigScript, "spinup-rollback-restore", "/opt/" + app + "/caddy"}})
				return errors.Join(switchErr, restoreErr)
			}
		}
		return switchErr
	}); err != nil {
		return err
	}
	if err := step("smoke", func() error {
		origin, _, err := publicOrigin(env.PublicURL, env.Host)
		if err != nil {
			return err
		}
		f := flow{o: o.Options, d: d, l: &local{app: app, env: env, origin: origin, sha: snap.GitSHA, web: web}, vm: vm, color: color, out: o.Out}
		_, err = f.smoke(ctx)
		return err
	}); err != nil {
		return err
	}
	if err := step("record", func() error {
		snap.Color = color
		data, _ := json.MarshalIndent(snap, "", "  ")
		if err := vm.Upload(ctx, append(data, '\n'), dir+"/release.json", 0644); err != nil {
			return err
		}
		kept := []ReleaseEntry{}
		for _, e := range entries {
			if e.Release == snap.Release.Release {
				e.Color = color
				kept = append(kept, e)
			}
		}
		for _, e := range entries {
			if e.Release != snap.Release.Release {
				kept = append(kept, e)
			}
		}
		if _, err := vm.Run(ctx, remote.Cmd{Argv: []string{"ln", "-sfn", "releases/" + snap.Release.Release, "/opt/" + app + "/current"}}); err != nil {
			return err
		}
		data, _ = json.MarshalIndent(kept, "", "  ")
		return vm.Upload(ctx, append(data, '\n'), "/opt/"+app+"/releases.json", 0644)
	}); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "Rolled back %s to release %s (%s color)\n", env.Name, snap.Release.Release, color)
	return nil
}

const rollbackColorScript = `set -eu
cd -- "$1"
test -f .env
sed "s/^SPINUP_COLOR=.*/SPINUP_COLOR=$2/" .env > .env.rollback
mv -f -- .env.rollback .env
caddy=/opt/$3/caddy
# Preserve the stable Caddy directory mount and never touch Postgres.
rm -rf -- "$caddy/.rollback"
mkdir -- "$caddy/.rollback"
cp -- "$caddy/Caddyfile" "$caddy/.rollback/Caddyfile"
if [ -d "$caddy/Caddyfile.d" ]; then cp -R -- "$caddy/Caddyfile.d" "$caddy/.rollback/Caddyfile.d"; fi
restore() {
    cp -- "$caddy/.rollback/Caddyfile" "$caddy/Caddyfile"
    rm -rf -- "$caddy/Caddyfile.d"
    if [ -d "$caddy/.rollback/Caddyfile.d" ]; then cp -R -- "$caddy/.rollback/Caddyfile.d" "$caddy/Caddyfile.d"; fi
}
trap restore EXIT
cp -- Caddyfile "$caddy/Caddyfile.next"
rm -rf -- "$caddy/Caddyfile.d"
if [ -d Caddyfile.d ]; then cp -R -- Caddyfile.d "$caddy/Caddyfile.d"; else mkdir -- "$caddy/Caddyfile.d"; fi
container=$(docker compose -p "$3" ps --status running -q caddy)
test -n "$container"
docker exec "$container" caddy validate --config /etc/caddy/Caddyfile.next --adapter caddyfile
mv -f -- "$caddy/Caddyfile.next" "$caddy/Caddyfile"
trap - EXIT`

const rollbackRestoreConfigScript = `set -eu
cp -- "$1/.rollback/Caddyfile" "$1/Caddyfile"
rm -rf -- "$1/Caddyfile.d"
if [ -d "$1/.rollback/Caddyfile.d" ]; then cp -R -- "$1/.rollback/Caddyfile.d" "$1/Caddyfile.d"; else mkdir -- "$1/Caddyfile.d"; fi`

func writeDBPlan(o RollbackOptions, app string, left *rollbackSnapshot) {
	fmt.Fprintln(o.ErrOut, "Database unchanged. Review the manual recovery plan with --with-db-plan.")
	if left.PreDeployBackupTime.IsZero() {
		fmt.Fprintln(o.ErrOut, "The release snapshot has no pre-deploy backup time. Obtain the verified backup time before recovery.")
		return
	}
	target := left.PreDeployBackupTime.UTC().Format(time.RFC3339)
	fmt.Fprintf(o.ErrOut, "Manual recovery plan for %s, target %s:\n", app, target)
	for _, line := range []string{"Stop application writes and isolate Postgres.", "Verify the pre-deploy backup and WAL archive for target " + target + ".", "Stop Postgres. Restore into a separate data directory with pgbackrest --stanza=" + app + " --type=time --target=" + remote.Quote(target) + " --target-action=promote --pg1-path='<isolated-data-directory>' restore.", "Start the restored database in isolation. Verify the data before switching connections.", "Resume application writes only after verification."} {
		fmt.Fprintln(o.ErrOut, strings.TrimSpace(line))
	}
}
