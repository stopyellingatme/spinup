package pgupgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/maintain/pg"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

// Run upgrades PostgreSQL with hard links under the shared maintenance guards.
func Run(ctx context.Context, o pg.Options, d pg.Deps) error {
	return pg.RunStrategy(ctx, o, d, workflow{})
}

type workflow struct{}

func (workflow) Plan(out io.Writer, cleanup bool) {
	steps := []string{"Check the maintenance window and protected confirmation", "Take the deploy lock"}
	if cleanup {
		steps = append(steps, "Verify the completed upgrade, active data directory, and repository changes", "Remove only the retained old data directory. Never start the old cluster")
	} else {
		steps = append(steps, "Build the helper image on the VM from both pinned PostgreSQL images", "Take a full backup with the configured backend", "Save recovery files and the exact backup restore command", "Stop api and web of both colors, then stop postgres", "Initialize the new data directory on the same volume", "Run pg_upgrade --check. Stop on any problem", "Run pg_upgrade --link. Never start the old cluster again", "Switch the PostgreSQL image and PGDATA", "Start postgres and upgrade the pgBackRest stanza, then take a full backup", "Run vacuumdb --all --analyze-in-stages", "Migrate, start the live color, and run the deploy smoke checks", "Commit the printed repository changes before the next deploy", "Retain the old data directory until --cleanup")
	}
	for _, step := range steps {
		fmt.Fprintln(out, "Plan: "+step)
	}
}

type state struct {
	Volume   string `json:"volume"`
	Old      string `json:"old"`
	New      string `json:"new"`
	From     int    `json:"from"`
	To       int    `json:"to"`
	Restore  string `json:"restore"`
	Complete bool   `json:"complete"`
}

func fail(step, why string) error {
	return fmt.Errorf("%s failed: %s\nTry: inspect the upgrade logs and restore the safety backup", step, why)
}

func oldPath(d pg.Details) (string, error) {
	if d.Volume == "" || (d.Mount != "/var/lib/postgresql/data" && d.Mount != "/var/lib/postgresql") || path.Clean(d.Data) != d.Data || !strings.HasPrefix(d.Data, d.Mount+"/") {
		return "", fail("inspect PostgreSQL layout", "PGDATA must be a subdirectory of one named PostgreSQL volume")
	}
	rel := strings.TrimPrefix(d.Data, d.Mount+"/")
	if !safePath(rel) {
		return "", fail("inspect PostgreSQL layout", "PGDATA contains an unsafe path")
	}
	return rel, nil
}

func safePath(s string) bool {
	if s == "" || s == "." || path.IsAbs(s) || path.Clean(s) != s {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." {
			return false
		}
		for _, c := range part {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
				return false
			}
		}
	}
	return true
}

func (workflow) Execute(ctx context.Context, s *pg.Session) (err error) {
	d := s.Details()
	image, err := s.Prepare()
	if err != nil {
		return err
	}
	if !validUser(d.User) {
		return fail("inspect runtime user", "expected a non-root numeric UID:GID")
	}
	old, err := oldPath(d)
	if err != nil {
		return err
	}
	previous, err := pinned(d.Current)
	if err != nil {
		return err
	}
	next := strconv.Itoa(d.To) + "/docker"
	if old == next || strings.HasPrefix(old, next+"/") || strings.HasPrefix(next, old+"/") {
		return fail("select data directory", "the old and new directories overlap")
	}
	helper := d.Config.App.Name + "-pg-upgrade:" + strconv.Itoa(d.Current) + "-" + strconv.Itoa(d.To)
	fmt.Fprintln(d.Out, "Build the helper image on the VM")
	dockerfile := helperDockerfile(previous.Ref(), image.Ref(), d.Current)
	helperFile := d.Dir + "/Dockerfile.pg-upgrade"
	if err = s.Upload(ctx, []byte(dockerfile), helperFile, 0600); err != nil {
		return err
	}
	if _, err = s.Run(ctx, "docker", "build", "--tag", helper, "--file", helperFile, d.Dir); err != nil {
		return err
	}
	fmt.Fprintln(d.Out, "Take a full backup with the configured backend")
	b, err := s.Backup(ctx)
	if err != nil {
		return err
	}
	if b.Label == "" || strings.ContainsAny(b.Label, "\x00\n\r") {
		return fail("record safety backup", "the backend returned an invalid backup label")
	}
	restore := "spinup backup restore --env " + remote.Quote(d.Env.Name) + " --label " + remote.Quote(b.Label) + " --into new"
	st := state{Volume: d.Volume, Old: old, New: next, From: d.Current, To: d.To, Restore: restore}
	refusal := "Never start the old cluster after pg_upgrade --link. Restore the safety backup instead:\n" + restore
	reserved, stopping, initializing, linking := false, false, false, false
	defer func() {
		if err == nil {
			return
		}
		if linking {
			fmt.Fprintln(d.Out, refusal)
			err = fmt.Errorf("%w\nTry: %s", err, restore)
		} else if reserved {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			if recoveryErr := recoverBeforeLink(cleanup, s, helper, st, stopping, initializing); recoveryErr != nil {
				err = fmt.Errorf("%w\nRecovery before pg_upgrade --link failed: %w\nTry: inspect the retained recovery files in %s before retrying", err, recoveryErr, d.OperationDir)
			} else {
				err = fmt.Errorf("%w\nTry: correct the upgrade failure and retry the same command", err)
			}
		}
	}()
	if err = s.SaveRecovery(ctx, ""); err != nil {
		return err
	}
	reserved = true
	if err = saveState(ctx, s, st); err != nil {
		return err
	}
	// All reads of the source cluster finish before downtime.
	settings, err := s.Query(ctx, "SHOW server_encoding;\nSHOW lc_collate;\nSHOW lc_ctype;\nSHOW data_checksums;\n")
	if err != nil {
		return err
	}
	values := strings.Split(strings.TrimSpace(string(settings)), "\n")
	if len(values) != 4 || (values[3] != "on" && values[3] != "off") {
		return fail("read cluster settings", "expected encoding, collation, ctype, and checksum settings")
	}
	user := s.DatabaseUser()
	if user == "" {
		return fail("read cluster user", "POSTGRES_USER is missing")
	}
	fmt.Fprintln(d.Out, "Stop api and web of both colors, then stop postgres")
	stopping = true // A lost SSH response can hide a successful stop.
	if err = s.StopColors(ctx); err != nil {
		return err
	}
	if _, err = s.Run(ctx, s.Base("stop", "postgres")...); err != nil {
		return err
	}
	fmt.Fprintln(d.Out, "Initialize the new data directory on the same volume")
	init := []string{"initdb", "--username=" + user, "--encoding=" + values[0], "--lc-collate=" + values[1], "--lc-ctype=" + values[2], "--auth-local=trust", "--auth-host=scram-sha-256", "-D", "/pg/" + next}
	if values[3] == "on" {
		init = append(init, "--data-checksums")
	} else if d.To >= 18 {
		init = append(init, "--no-data-checksums")
	}
	initializing = true
	if _, err = s.Run(ctx, helperArgs(d, helper, initScript, append([]string{old, next}, init...)...)...); err != nil {
		return err
	}
	upgrade := []string{"pg_upgrade", "--old-bindir=/usr/lib/postgresql/" + strconv.Itoa(d.Current) + "/bin", "--new-bindir=/usr/lib/postgresql/" + strconv.Itoa(d.To) + "/bin", "--old-datadir=/pg/" + old, "--new-datadir=/pg/" + next, "--username=" + user, "--socketdir=/tmp", "--old-options=-c archive_mode=off", "--new-options=-c archive_mode=off", "--link"}
	fmt.Fprintln(d.Out, "Run pg_upgrade --check")
	if _, err = s.Run(ctx, helperArgs(d, helper, execScript, append(upgrade, "--check")...)...); err != nil {
		return err
	}
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' " + remote.Quote(refusal) + " >&2\nexit 1\n"
	if err = s.Upload(ctx, []byte(script), d.OperationDir+"/rollback.sh", 0700); err != nil {
		return err
	}
	fmt.Fprintln(d.Out, "Run pg_upgrade --link. Never start the old cluster again")
	linking = true // An unsuccessful response does not prove that linking never started.
	if _, err = s.Run(ctx, helperArgs(d, helper, execScript, upgrade...)...); err != nil {
		return err
	}
	// Disable the original cluster even if a later switch or health check fails.
	if _, err = s.Run(ctx, helperArgs(d, helper, disableScript, old)...); err != nil {
		return err
	}
	if _, err = s.Run(ctx, "docker", "rm", d.ID); err != nil {
		return err
	}
	data := "/var/lib/postgresql/data/" + next
	if d.To == 18 {
		data = "/var/lib/postgresql/" + next
	}
	if err = s.Switch(ctx, image, d.Volume, data); err != nil {
		return err
	}
	fmt.Fprintln(d.Out, "Run vacuumdb --all --analyze-in-stages")
	if _, err = s.Run(ctx, s.Base("exec", "-T", "postgres", "vacuumdb", "--all", "--analyze-in-stages", "--username="+user)...); err != nil {
		return err
	}
	if err = s.Finish(ctx); err != nil {
		return err
	}
	st.Complete = true
	if err = saveState(ctx, s, st); err != nil {
		return err
	}
	s.RepositoryChange(d.Volume)
	fmt.Fprintf(d.Out, "Set services.postgres.environment.PGDATA to %s in deploy/compose.edge.yml.\n", data)
	if d.To != 18 {
		fmt.Fprintf(d.Out, "Set pg1-path=%s in the pgBackRest configuration when applicable.\n", data)
	}
	fmt.Fprintln(d.Out, "Never start the old cluster. Restore from the safety backup instead:")
	fmt.Fprintln(d.Out, restore)
	fmt.Fprintf(d.Out, "Retained old directory: %s on volume %s\n", old, d.Volume)
	fmt.Fprintf(d.Out, "Remove it after verification: spinup maintenance pg-upgrade-in-place --env %s --cleanup --i-know\n", d.Env.Name)
	return nil
}

func saveState(ctx context.Context, s *pg.Session, st state) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.Upload(ctx, data, s.Details().OperationDir+"/link-state.json", 0600)
}

func pinned(major int) (stackversions.Image, error) {
	key := "postgres"
	if major == 16 || major == 17 {
		key += "-" + strconv.Itoa(major)
	} else if major != 18 {
		return stackversions.Image{}, fail("select source image", "the running major has no pin")
	}
	return stackversions.MustLoad().Image(key)
}
