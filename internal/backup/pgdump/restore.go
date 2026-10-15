package pgdump

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func (p *PGDump) target(ctx context.Context, t backup.Target) (backup.Info, error) {
	if !t.Time.IsZero() {
		return backup.Info{}, fmt.Errorf("select pgdump target: logical dumps do not support point-in-time recovery\nTry: select a dump label from spinup backup list")
	}
	if t.Label != "" {
		if _, err := labelTime(t.Label); err != nil {
			return backup.Info{}, err
		}
	}
	files, err := p.List(ctx)
	if err != nil {
		return backup.Info{}, err
	}
	if len(files) == 0 {
		return backup.Info{}, fmt.Errorf("select pgdump target: no complete dump exists\nTry: run spinup backup now --type full")
	}
	if t.Label == "" {
		return files[len(files)-1], nil
	}
	for _, f := range files {
		if f.Label == t.Label {
			return f, nil
		}
	}
	return backup.Info{}, fmt.Errorf("select pgdump target: dump %q does not exist\nTry: select a label from spinup backup list", t.Label)
}

func (p *PGDump) database(ctx context.Context) (string, string, error) {
	values := make([]string, 2)
	for i, key := range []string{"POSTGRES_DB", "POSTGRES_USER"} {
		out, err := p.run(ctx, "read postgres environment", p.postgres("printenv", key)...)
		if err != nil {
			return "", "", err
		}
		values[i] = strings.TrimSuffix(string(out), "\n")
		if values[i] == "" || strings.ContainsAny(values[i], "\x00\n\r") {
			return "", "", fmt.Errorf("read postgres environment: %s is empty or invalid\nTry: set %s in the postgres service environment", key, key)
		}
	}
	return values[0], values[1], nil
}

func (p *PGDump) image(ctx context.Context) (string, error) {
	out, err := p.run(ctx, "find the postgres container", p.compose("ps", "--status", "running", "-q", "postgres")...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" || strings.ContainsAny(id, "\n\r") {
		return "", fmt.Errorf("find the postgres container: expected one running service\nTry: start the base postgres service before the restore")
	}
	out, err = p.run(ctx, "read the postgres image", "docker", "inspect", "--format", "{{.Image}}", "--", id)
	if err != nil {
		return "", err
	}
	image := strings.TrimSpace(string(out))
	if image == "" || strings.ContainsAny(image, "\n\r") {
		return "", fmt.Errorf("read the postgres image: Docker returned no image ID\nTry: inspect the running postgres container on the VM")
	}
	return image, nil
}

const cleanupScript = `id=$(docker ps -aq --filter "name=^/$1$" --filter "label=spinup.pgdump.operation=$2")
if [ -n "$id" ]; then docker rm -f -v -- "$id"; fi`

func (p *PGDump) cleanup(ctx context.Context, name, owner string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, err := p.run(ctx, "remove the restore container and its volume", "sh", "-eu", "-c", cleanupScript, "pgdump", name, owner)
	return err
}

// restoredContainer creates an isolated database. Only a successful new restore survives.
func (p *PGDump) restoredContainer(ctx context.Context, f backup.Info, keep bool) (result backup.VerifyResult, err error) {
	result.Label = f.Label
	db, user, err := p.database(ctx)
	if err != nil {
		return result, err
	}
	image, err := p.image(ctx)
	if err != nil {
		return result, err
	}
	mode := "verify"
	if keep {
		mode = "restore"
	}
	name := p.Env.App + "-" + mode + "-" + f.Label
	owner := p.Now().UTC().Format(time.RFC3339Nano)
	leave := false
	// The ownership label prevents cleanup from removing a pre-existing container.
	// Register before creation because cancellation can lose Docker's successful reply.
	defer func() {
		if !leave {
			err = errors.Join(err, p.cleanup(ctx, name, owner))
			if err != nil {
				result.OK = false
			}
		}
	}()
	network := "none"
	if keep {
		network = "bridge"
	}
	args := []string{"docker", "create", "--name", name, "--pull", "never", "--network", network, "--label", "spinup.pgdump.operation=" + owner, "--env", "POSTGRES_USER=" + user, "--env", "POSTGRES_DB=" + db, "--env", "POSTGRES_HOST_AUTH_METHOD=trust"}
	if keep {
		args = append(args, "--publish", "127.0.0.1:5433:5432")
	}
	// Disable WAL archiving in this independent database.
	args = append(args, image, "postgres", "-c", "archive_mode=off")
	if _, err = p.run(ctx, "create the restore container", args...); err != nil {
		return result, err
	}
	if _, err = p.run(ctx, "start the restore container", "docker", "start", name); err != nil {
		return result, err
	}
	if _, err = p.run(ctx, "wait for the restore database", "sh", "-eu", "-c", readyScript, "pgdump", name, user, db); err != nil {
		return result, err
	}
	start := p.Now()
	if _, err = p.run(ctx, "restore the dump", "sh", "-eu", "-c", containerRestoreScript, "pgdump", path.Join(p.directory(), f.Label+".dump"), name, user, db); err != nil {
		return result, err
	}
	if keep {
		if err = p.say("Restored database %s in %s.\n%s\n", db, name, p.tunnel()); err != nil {
			return result, err
		}
		leave = true
		return result, nil
	}
	out, err := p.run(ctx, "query the restored goose version", "docker", "exec", "--user", "postgres", name, "psql", "-X", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-U", user, "-d", db, "-c", "SELECT count(*) FROM goose_db_version")
	if err != nil {
		return result, err
	}
	elapsed := p.Now().Sub(start)
	count, parseErr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if parseErr != nil || count < 0 {
		return result, fmt.Errorf("check the restored goose version: the query returned %q\nTry: inspect goose_db_version in the selected dump", out)
	}
	result.OK = true
	result.RTO = elapsed
	return result, nil
}

const readyScript = `i=0
until docker exec --user postgres "$1" pg_isready -q -h 127.0.0.1 -t 1 -U "$2" -d "$3"; do
 i=$((i + 1))
 [ "$i" -lt 60 ] || { echo 'postgres did not become ready after 60 attempts' >&2; exit 1; }
 sleep 1
done`

const containerRestoreScript = `exec docker exec -i --user postgres "$2" pg_restore --exit-on-error --clean --if-exists -U "$3" -d "$4" < "$1"`

// Verify restores the selected dump, queries goose_db_version, and removes all temporary resources.
func (p *PGDump) Verify(ctx context.Context, t backup.Target) (backup.VerifyResult, error) {
	f, err := p.target(ctx, t)
	if err != nil {
		return backup.VerifyResult{}, err
	}
	return p.restoredContainer(ctx, f, false)
}

// Restore restores a dump into a new container or replaces the live database.
// The caller owns the deploy lock, confirmation, and app shutdown.
func (p *PGDump) Restore(ctx context.Context, t backup.Target, into backup.Into) error {
	if into.New == into.InPlace {
		return fmt.Errorf("restore pgdump: select exactly one destination\nTry: pass --into new or --into in-place")
	}
	f, err := p.target(ctx, t)
	if err != nil {
		return err
	}
	if into.New {
		_, err = p.restoredContainer(ctx, f, true)
		return err
	}
	return p.inPlace(ctx, f)
}

const inPlaceRestoreScript = `file=$1
shift
exec "$@" < "$file"`

// Each statement is separate because ALTER DATABASE RENAME cannot run in a transaction.
const swapSQL = `\set ON_ERROR_STOP on
ALTER DATABASE :"db" WITH ALLOW_CONNECTIONS false;
SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = :'db' AND pid <> pg_backend_pid();
ALTER DATABASE :"db" RENAME TO :"old";
ALTER DATABASE :"restored" RENAME TO :"db";
ALTER DATABASE :"old" WITH ALLOW_CONNECTIONS true;
`

func (p *PGDump) inPlace(ctx context.Context, f backup.Info) error {
	db, user, err := p.database(ctx)
	if err != nil {
		return err
	}
	maintenance := "postgres"
	if db == maintenance {
		maintenance = "template1"
	}
	restored, old := db+"_restore", db+"_old_"+f.Label
	if len(restored) > 63 || len(old) > 63 {
		return fmt.Errorf("restore pgdump in place: the database names exceed PostgreSQL's 63-byte limit\nTry: restore with --into new")
	}
	oldExists, err := p.sql(ctx, "check the old database name", maintenance, user, []string{"old=" + old}, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'old');\n")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(oldExists)) != "f" {
		return fmt.Errorf("prepare the database swap: old database %q already exists or its name could not be checked\nTry: inspect the old database and remove it before retrying if no longer needed", old)
	}
	if _, err = p.run(ctx, "create the staging database", p.postgres("createdb", "-U", user, "--maintenance-db="+maintenance, "--", restored)...); err != nil {
		return fmt.Errorf("prepare the staging database %q: %w\nTry: inspect the staging database and remove it before retrying if no longer needed", restored, err)
	}
	args := []string{"sh", "-eu", "-c", inPlaceRestoreScript, "pgdump", path.Join(p.directory(), f.Label+".dump")}
	args = append(args, p.postgres("pg_restore", "--exit-on-error", "--clean", "--if-exists", "-U", user, "-d", restored)...)
	if _, err = p.run(ctx, "restore the staging database", args...); err != nil {
		return fmt.Errorf("restore staging database %q before the live swap: %w\nTry: inspect or remove the staging database before retrying", restored, err)
	}
	if _, err = p.sql(ctx, "rename the restored database", maintenance, user, []string{"db=" + db, "restored=" + restored, "old=" + old}, swapSQL); err != nil {
		return fmt.Errorf("rename the restored database: %w (live=%q, staging=%q, old=%q)\nTry: inspect these databases with psql and complete the renames before restarting the app", err, db, restored, old)
	}
	return p.say("Restored database %s. After checking the app, remove the old database:\n%s\n", db, shellLine(p.postgres("dropdb", "-U", user, "--", old)))
}

func (p *PGDump) sql(ctx context.Context, what, db, user string, variables []string, script string) ([]byte, error) {
	args := []string{"psql", "-X", "-t", "-A", "-U", user, "-d", db, "-v", "ON_ERROR_STOP=1"}
	for _, variable := range variables {
		args = append(args, "-v", variable)
	}
	args = append(args, "-f", "-")
	result, err := p.Env.Remote.Run(ctx, remote.Cmd{Argv: p.postgres(args...), Stdin: strings.NewReader(script), Stderr: p.progress()})
	if err != nil {
		return nil, fmt.Errorf("%s: %w\nTry: inspect the database with psql before retrying the restore", what, err)
	}
	return result.Stdout, nil
}

func shellLine(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = remote.Quote(arg)
	}
	return strings.Join(parts, " ")
}

func (p *PGDump) tunnel() string {
	e := p.Env.Environment
	destination := e.Host
	if destination == "" {
		destination = "<host>"
	}
	if e.User != "" {
		destination = e.User + "@" + destination
	}
	args := []string{"ssh", "-N", "-L", "5433:127.0.0.1:5433"}
	if e.Port != 0 {
		args = append(args, "-p", strconv.Itoa(e.Port))
	}
	// server ssh currently accepts neither forwarding flags nor arguments after --.
	return "Open a tunnel from your computer: " + strings.Join(args, " ") + " -- " + remote.Quote(destination)
}

func (p *PGDump) say(format string, args ...any) error {
	if _, err := fmt.Fprintf(p.progress(), format, args...); err != nil {
		return fmt.Errorf("print restore instructions: %w\nTry: inspect the restore container or database on the VM", err)
	}
	return nil
}
