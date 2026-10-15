package pg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

const snapshotSQL = `SELECT id, version_id, is_applied FROM goose_db_version ORDER BY id;
SELECT format('SELECT json_build_array(%L, count(*))::text FROM %I.%I;', schemaname || '.' || tablename, schemaname, tablename)
FROM pg_tables
WHERE schemaname <> 'information_schema' AND schemaname !~ '^pg_'
ORDER BY schemaname, tablename
\gexec
`
const readyScript = `i=0
until pg_isready -q -h 127.0.0.1 -t 1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"; do
 i=$((i+1)); [ "$i" -lt 120 ] || exit 1; sleep 1
done`

func (u *upgrade) major(ctx context.Context) error {
	image, err := targetImage(u.current.major, u.o.To)
	if err != nil {
		return err
	}
	_, err = u.majorBackupConfig()
	if err != nil {
		return err
	}
	oldVolume := u.volume()
	if oldVolume == "" {
		return failure("prepare major upgrade", errors.New("the old server has no named data volume"))
	}
	// Validate smoke configuration before stopping the app.
	if _, err = deploy.LoadEnvFile(u.root, u.env.Name); err != nil {
		return err
	}
	if _, err = u.publicOrigin(); err != nil {
		return err
	}
	if err = u.saveRecovery(ctx, oldVolume); err != nil {
		return err
	}
	fmt.Fprintln(u.o.Out, "Stop api and web of both colors. The app is down.")
	if _, err = u.run(ctx, "sh", "-ec", stopColorsScript(u.cfg.App.Name)); err != nil {
		return err
	}
	fmt.Fprintln(u.o.Out, "Take a full backup with pgdump")
	dump, err := u.backup(ctx, "pgdump")
	if err != nil {
		return err
	}
	old, err := u.sql(ctx, u.live.ID, snapshotSQL)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(old)) == 0 {
		return failure("snapshot old database", errors.New("the migration and row-count report is empty"))
	}
	volume := fmt.Sprintf("%s_pgdata_%d", u.cfg.App.Name, u.o.To)
	name := fmt.Sprintf("%s-pg-upgrade-%d", u.cfg.App.Name, u.o.To)
	fmt.Fprintf(u.o.Out, "Start the target pinned image on new volume %s\n", volume)
	reserve := `if docker volume inspect "$1" >/dev/null 2>&1; then echo 'target volume already exists' >&2; exit 1; fi
exec docker volume create "$1"`
	if _, err = u.run(ctx, "sh", "-ec", reserve, "pg-upgrade", volume); err != nil {
		return err
	}
	mount := "/var/lib/postgresql/data"
	pgdata := mount + "/pgdata"
	if u.o.To == 18 {
		mount = "/var/lib/postgresql"
		pgdata = mount + "/18/docker"
	}
	args := []string{"docker", "run", "-d", "--name", name, "--network", "none", "--mount", "type=volume,src=" + volume + ",dst=" + mount, "--mount", "type=bind,src=/opt/" + u.cfg.App.Name + "/secrets,dst=/run/secrets,readonly", "--env", "PGDATA=" + pgdata, "--env", "POSTGRES_PASSWORD_FILE=/run/secrets/POSTGRES_PASSWORD"}
	for _, key := range []string{"POSTGRES_USER=", "POSTGRES_DB="} {
		found := false
		for _, value := range u.live.Config.Env {
			if strings.HasPrefix(value, key) && len(value) > len(key) {
				args = append(args, "--env", value)
				found = true
				break
			}
		}
		if !found {
			return failure("create target server", fmt.Errorf("%s is missing", key))
		}
	}
	init := `mkdir -p "$PGDATA"; chown -R "$1" "$2"; exec gosu "$1" docker-entrypoint.sh postgres -c archive_mode=off`
	args = append(args, "--entrypoint", "sh", image.Ref(), "-ec", init, "pg-upgrade", u.live.Config.User, mount)
	if _, err = u.run(ctx, args...); err != nil {
		return err
	}
	// Retain the new volume on failure. Only the staging container is temporary.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, e := u.run(cleanup, "docker", "rm", "-f", name); e != nil {
			fmt.Fprintf(u.o.ErrOut, "Remove the staging container after inspection: %s\n", shell("docker", "rm", "-f", name))
		}
	}()
	if _, err = u.run(ctx, "docker", "exec", name, "sh", "-ec", readyScript); err != nil {
		return err
	}
	fmt.Fprintln(u.o.Out, "Restore the dump and run ANALYZE")
	restore := `exec docker exec -i "$2" sh -ec 'exec pg_restore --exit-on-error --clean --if-exists -U "$POSTGRES_USER" -d "$POSTGRES_DB"' < "$1"`
	if _, err = u.run(ctx, "sh", "-ec", restore, "pg-upgrade", "/opt/"+u.cfg.App.Name+"/backups/pgdump/"+dump.Label+".dump", name); err != nil {
		return err
	}
	if _, err = u.sql(ctx, name, "ANALYZE;\n"); err != nil {
		return err
	}
	fmt.Fprintln(u.o.Out, "Compare goose_db_version and every user table row count")
	restored, err := u.sql(ctx, name, snapshotSQL)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(old), bytes.TrimSpace(restored)) {
		return failure("verify restored database", errors.New("goose_db_version or user table row counts differ. The swap was not started"))
	}
	fmt.Fprintln(u.o.Out, "Stop the old postgres and swap the Compose image and volume")
	if _, err = u.run(ctx, "docker", "stop", name); err != nil {
		return err
	}
	if _, err = u.run(ctx, u.base("stop", "postgres")...); err != nil {
		return err
	}
	if err = u.switchMajor(ctx, image, volume, "", false); err != nil {
		return err
	}
	if err = u.finish(ctx); err != nil {
		return err
	}
	state := upgradeState{OldVolume: oldVolume, NewVolume: volume, Anchor: fmt.Sprintf("%s-pg-recovery-%d-%d", u.cfg.App.Name, u.current.major, u.current.minor), Complete: true}
	if err = u.writeState(ctx, state); err != nil {
		return err
	}
	fmt.Fprintf(u.o.Out, "PostgreSQL now runs major %d. Retained old volume: %s\n", u.o.To, oldVolume)
	fmt.Fprintf(u.o.Out, "Remove it after verification: spinup maintenance pg-major-upgrade --env %s --cleanup --i-know\n", u.env.Name)
	fmt.Fprintln(u.o.Out, "Recovery returns to the pre-upgrade database. It discards writes made after the swap.")
	u.repositoryChange()
	return nil
}

func (u *upgrade) finish(ctx context.Context) error {
	var err error
	fmt.Fprintln(u.o.Out, "Run migrate with the live release")
	if _, err = u.run(ctx, u.color("--profile", "migrate", "run", "--rm", "-T", "migrate")...); err != nil {
		return err
	}
	fmt.Fprintln(u.o.Out, "Start the live color")
	if _, err = u.run(ctx, u.color(append([]string{"up", "-d", "--no-deps", "--wait"}, u.services()...)...)...); err != nil {
		return err
	}
	fmt.Fprintln(u.o.Out, "Run the deploy smoke checks")
	if err = u.smoke(ctx); err != nil {
		return err
	}

	return nil
}

func (u *upgrade) switchMajor(ctx context.Context, image stackversions.Image, volume, dataPath string, fullBackup bool) error {
	if err := u.updateComposeAt(ctx, image, volume, dataPath); err != nil {
		return err
	}
	if _, err := u.run(ctx, u.base("build", "postgres")...); err != nil {
		return err
	}
	config, err := u.majorBackupConfig()
	if err != nil {
		return err
	}
	if config != nil {
		// Preserve every repository option while replacing the standard target data path.
		standard := "/var/lib/postgresql/data/pgdata"
		if u.o.To == 18 {
			standard = "/var/lib/postgresql/18/docker"
		}
		if dataPath != "" {
			config = []byte(strings.Replace(string(config), "pg1-path="+standard, "pg1-path="+dataPath, 1))
		}
		if err = u.vm.Upload(ctx, config, backup.RemoteConfigPath(u.cfg.App.Name), 0600); err != nil {
			return failure("write pgBackRest configuration for the new major", err)
		}
	}
	if _, err = u.run(ctx, u.base("up", "-d", "--no-deps", "--no-build", "--force-recreate", "--wait", "--wait-timeout", "120", "postgres")...); err != nil {
		return err
	}
	if config != nil {
		fmt.Fprintln(u.o.Out, "Upgrade the pgBackRest stanza for the new PostgreSQL cluster")
		if _, err = u.run(ctx, u.base(backup.Command(u.cfg.App.Name, "stanza-upgrade")[4:]...)...); err != nil {
			return failure("upgrade pgBackRest stanza (WAL archiving requires repair)", err)
		}
		if fullBackup {
			if _, err = u.backupAt(ctx, "", config); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(u.o.ErrOut, "WARNING: Take a new full backup after the repository changes below: spinup backup now --env %s --type full\n", u.env.Name)
		}
	}
	return nil
}
