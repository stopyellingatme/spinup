package walg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
)

type postgres struct {
	Image  string
	Config struct {
		User string
		Env  []string
	}
	Mounts                []struct{ Destination string }
	data, mount, db, user string
}

func (w *WALG) inspect(ctx context.Context) (postgres, error) {
	var p postgres
	out, err := w.run(ctx, "find postgres", w.compose("ps", "-aq", "postgres")...)
	if err != nil {
		return p, err
	}
	ids := strings.Fields(string(out))
	if len(ids) != 1 {
		return p, fmt.Errorf("inspect postgres: expected one container\nTry: deploy the postgres service before restoring")
	}
	out, err = w.run(ctx, "inspect postgres", "docker", "inspect", "--format", "{{json .}}", ids[0])
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(out, &p); err != nil {
		return p, fmt.Errorf("parse postgres inspection: %w\nTry: inspect the postgres container on the VM", err)
	}
	for _, env := range p.Config.Env {
		k, v, _ := strings.Cut(env, "=")
		switch k {
		case "PGDATA":
			p.data = v
		case "POSTGRES_DB":
			p.db = v
		case "POSTGRES_USER":
			p.user = v
		}
	}
	for _, m := range p.Mounts {
		if (m.Destination == "/var/lib/postgresql" || m.Destination == "/var/lib/postgresql/data") && strings.HasPrefix(p.data, m.Destination+"/") {
			p.mount = m.Destination
		}
	}
	if p.Image == "" || p.Config.User == "" || p.mount == "" || path.Clean(p.data) != p.data || p.db == "" || p.user == "" || strings.ContainsAny(p.data+p.db+p.user, "\x00\r\n") {
		return p, fmt.Errorf("inspect postgres: image, user, database, or PGDATA mount is invalid\nTry: restore the generated postgres service configuration")
	}
	return p, nil
}

// recoveryScript restores an empty PGDATA and replaces any recovery settings from the backup.
const recoveryScript = `wal-g backup-fetch "$PGDATA" "$1"
# Remove settings inherited from a previous recovery.
if [ -f "$PGDATA/postgresql.auto.conf" ]; then
 sed '/^[[:space:]]*recovery_target/d; /^[[:space:]]*restore_command[[:space:]]*=/d' "$PGDATA/postgresql.auto.conf" > "$PGDATA/spinup.auto.conf"
 mv "$PGDATA/spinup.auto.conf" "$PGDATA/postgresql.auto.conf"
fi
printf '%s\n' 'restore_command = '\''wal-g wal-fetch "%f" "%p"'\''' "recovery_target_timeline = 'latest'" "recovery_target_action = 'promote'" >> "$PGDATA/postgresql.auto.conf"
if [ -n "$2" ]; then
 printf "recovery_target_time = '%s'\n" "$2" >> "$PGDATA/postgresql.auto.conf"
else
 printf "recovery_target = 'immediate'\n" >> "$PGDATA/postgresql.auto.conf"
fi
rm -f "$PGDATA/standby.signal"
touch "$PGDATA/recovery.signal"
`

func recoveryTime(t backup.Target) string {
	if t.Time.IsZero() {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339Nano)
}

const checkScript = `set -eu
i=0
until pg_isready -U "$1" -d "$2" >/dev/null 2>&1 && [ "$(psql -U "$1" -d "$2" -XAt -v ON_ERROR_STOP=1 -c 'SELECT pg_is_in_recovery()')" = f ]; do
 i=$((i+1)); [ "$i" -lt 120 ] || { echo 'postgres recovery did not finish' >&2; exit 1; }; sleep 1
done
exec psql -U "$1" -d "$2" -XAt -v ON_ERROR_STOP=1 -c 'SELECT count(*) FROM goose_db_version'`

func checkCount(out []byte) error {
	n, err := strconv.ParseInt(text(out), 10, 64)
	if err != nil || n < 0 {
		return fmt.Errorf("check restored goose_db_version: invalid query result\nTry: inspect goose_db_version in the restored database")
	}
	return nil
}

const cleanupScript = `id=$(docker ps -aq --filter "name=^/$1$" --filter "label=spinup.walg.operation=$2")
if [ -n "$id" ]; then docker rm -fv -- "$id"; fi`

func (w *WALG) restoredContainer(ctx context.Context, b backup.Info, t backup.Target, keep bool) (result backup.VerifyResult, err error) {
	result.Label = b.Label
	p, err := w.inspect(ctx)
	if err != nil {
		return result, err
	}
	purpose := "verify"
	if keep {
		purpose = "restore"
	}
	name := w.Env.App + "-" + purpose + "-" + b.Label
	owner := w.Now().UTC().Format(time.RFC3339Nano)
	start := w.Now()
	leave := false
	defer func() {
		result.RTO = w.Now().Sub(start)
		if !leave {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_, cleanupErr := w.run(cleanup, "remove WAL-G restore container", "sh", "-eu", "-c", cleanupScript, "walg", name, owner)
			err = errors.Join(err, cleanupErr)
		}
		if err != nil {
			result.OK = false
		}
	}()
	root := "/opt/" + w.Env.App
	args := []string{"docker", "create", "--name", name, "--pull", "never", "--network", w.Env.App + "-backup", "--label", "spinup.walg.operation=" + owner, "--env", "PGDATA=" + p.data, "--mount", "type=volume,dst=" + p.mount, "--mount", "type=bind,src=" + root + "/secrets,dst=/run/secrets,readonly"}
	if keep {
		args = append(args, "--publish", "127.0.0.1:5433:5432")
	}
	// Initialize volume ownership as root, then run WAL-G and postgres as the deploy user.
	entry := `set -eu; mkdir -p "$PGDATA"; chmod 0700 "$PGDATA"; chown -R "$1" "$2"; user=$1; shift 2; exec gosu "$user" sh -c "$@"`
	recovery := SecretLoader + recoveryScript + `exec postgres -c archive_mode=off -c listen_addresses='*'`
	args = append(args, "--entrypoint", "sh", p.Image, "-c", entry, "walg", p.Config.User, p.mount, recovery, "walg", b.Label, recoveryTime(t))
	if _, err = w.run(ctx, "create WAL-G restore container", args...); err != nil {
		return result, err
	}
	if _, err = w.run(ctx, "start WAL-G recovery", "docker", "start", name); err != nil {
		return result, err
	}
	out, err := w.run(ctx, "check WAL-G recovery", "docker", "exec", "--user", p.Config.User, name, "sh", "-c", checkScript, "walg", p.user, p.db)
	if err != nil {
		return result, err
	}
	if err = checkCount(out); err != nil {
		return result, err
	}
	result.OK = true
	leave = keep
	return result, nil
}

func (w *WALG) inPlace(ctx context.Context, b backup.Info, t backup.Target) (err error) {
	p, err := w.inspect(ctx)
	if err != nil {
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
			_, restartErr := w.run(cleanup, "restart postgres", w.compose("start", "--wait", "postgres")...)
			err = errors.Join(err, restartErr)
		} else {
			err = fmt.Errorf("%w\nThe live database may be partially restored. The previous data remains at %s.spinup-before-restore.\nTry: inspect the database before starting postgres", err, p.data)
		}
	}()
	stopping = true
	if _, err = w.run(ctx, "stop postgres", w.compose("stop", "postgres")...); err != nil {
		return err
	}
	// Keep the previous data. Refuse another replacement until the operator removes it.
	script := SecretLoader + `[ ! -e "$PGDATA.spinup-before-restore" ] && [ ! -L "$PGDATA" ]
mv "$PGDATA" "$PGDATA.spinup-before-restore"
mkdir -m 0700 "$PGDATA"
` + recoveryScript
	touched = true // A lost SSH response can hide a successful volume mutation.
	args := w.compose("run", "--rm", "--no-deps", "-T", "--entrypoint", "sh", "postgres", "-c", script, "walg", b.Label, recoveryTime(t))
	if _, err = w.run(ctx, "restore live WAL-G data", args...); err != nil {
		return err
	}
	if _, err = w.run(ctx, "start restored postgres", w.compose("start", "--wait", "postgres")...); err != nil {
		return err
	}
	out, err := w.run(ctx, "check restored postgres", w.compose("exec", "-T", "postgres", "sh", "-c", checkScript, "walg", p.user, p.db)...)
	if err != nil {
		return err
	}
	return checkCount(out)
}
