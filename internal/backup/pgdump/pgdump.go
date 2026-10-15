package pgdump

import (
	"context"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/remote"
)

const labelLayout = "20060102T150405Z"

var appName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func init() { backup.RegisterBackend("pgdump", New) }

// PGDump runs pg_dump and pg_restore through the deployment VM.
// Construct it with New before use.
type PGDump struct {
	// Env supplies the app, remote runner, and output stream.
	Env backup.Env
	// Retention is the number of dumps to keep. New defaults it to seven.
	Retention int
	// Now supplies the clock for labels and elapsed restore time.
	Now func() time.Time
}

var _ backup.Backend = (*PGDump)(nil)

// New creates a pgdump backend from the environment and retention configuration.
func New(e backup.Env) (backup.Backend, error) {
	if !appName.MatchString(e.App) {
		return nil, fmt.Errorf("initialize pgdump: app name %q is not a valid Compose project name\nTry: use lowercase letters, digits, underscores, and dashes", e.App)
	}
	if e.Remote == nil {
		return nil, fmt.Errorf("initialize pgdump: the remote runner is missing\nTry: select a configured deployment environment")
	}
	retention := 7
	if e.Config != nil && e.Config.Backups.Retention != "" {
		n, err := strconv.Atoi(e.Config.Backups.Retention)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("initialize pgdump: retention %q must be a positive integer\nTry: set [backups] retention to \"7\" in spinup.toml", e.Config.Backups.Retention)
		}
		retention = n
	}
	return &PGDump{Env: e, Retention: retention, Now: time.Now}, nil
}

func (p *PGDump) directory() string { return path.Join("/opt", p.Env.App, "backups/pgdump") }

func (p *PGDump) progress() io.Writer {
	if p.Env.Progress == nil {
		return io.Discard
	}
	return p.Env.Progress
}

func (p *PGDump) run(ctx context.Context, what string, argv ...string) ([]byte, error) {
	result, err := p.Env.Remote.Run(ctx, remote.Cmd{Argv: argv, Stderr: p.progress()})
	if err != nil {
		return nil, fmt.Errorf("%s on the VM: %w\nTry: inspect spinup backup status and the postgres container", what, err)
	}
	return result.Stdout, nil
}

// compose uses the current release without depending on the SSH working directory.
func (p *PGDump) compose(args ...string) []string {
	dir := path.Join("/opt", p.Env.App, "current")
	return append([]string{"docker", "compose", "-p", p.Env.App, "--project-directory", dir, "-f", dir + "/compose.edge.yml"}, args...)
}

func (p *PGDump) postgres(args ...string) []string {
	return p.compose(append([]string{"exec", "-T", "--user", "postgres", "postgres"}, args...)...)
}

// Backup writes a full custom-format dump and publishes it with an atomic rename.
// kind is ignored because logical dumps are always full backups.
func (p *PGDump) Backup(ctx context.Context, _ backup.Kind) (backup.Result, error) {
	start := p.Now().UTC()
	label := start.Format(labelLayout) + "F"
	out, err := p.run(ctx, "create pgdump backup", "sh", "-eu", "-c", backupScript, "pgdump", p.Env.App, p.directory(), label)
	if err != nil {
		return backup.Result{}, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || size < 0 {
		return backup.Result{}, fmt.Errorf("read pgdump size: stat returned %q\nTry: inspect the dump with stat on the VM", out)
	}
	stop := p.Now().UTC()
	return backup.Result{Label: label, Kind: backup.Full, Start: start, Stop: stop, Duration: stop.Sub(start), Size: size, RepoSize: size}, nil
}

const backupScript = `umask 077
app=$1
dir=$2
label=$3
mkdir -p -- "$dir"
chmod 700 "$dir"
tmp=$dir/$label.dump.tmp
dest=$dir/$label.dump
[ ! -e "$dest" ] || { echo 'dump label already exists' >&2; exit 1; }
(set -C; : > "$tmp")
trap 'rm -f -- "$tmp"' EXIT
trap 'exit 1' HUP INT TERM
docker compose -p "$app" --project-directory "/opt/$app/current" -f "/opt/$app/current/compose.edge.yml" exec -T --user postgres postgres sh -eu -c '
 db=$(printenv POSTGRES_DB)
 user=$(printenv POSTGRES_USER)
 [ -n "$db" ] && [ -n "$user" ]
 exec pg_dump -Fc -U "$user" --dbname "$db"
' > "$tmp"
chmod 600 "$tmp"
mv -- "$tmp" "$dest"
stat -c %s -- "$dest"`

const listScript = `if [ ! -d "$1" ]; then printf 'missing\n'; exit 0; fi
printf 'exists\n'
for f in "$1"/*.dump; do
 [ -f "$f" ] || continue
 stat --printf '%n\t%s\t%Y\n' -- "$f"
done`

func labelTime(label string) (time.Time, error) {
	if len(label) != 17 || !strings.HasSuffix(label, "F") {
		return time.Time{}, fmt.Errorf("invalid dump label %q\nTry: select a label from spinup backup list", label)
	}
	t, err := time.Parse(labelLayout, strings.TrimSuffix(label, "F"))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse dump label %q: %w\nTry: select a label from spinup backup list", label, err)
	}
	return t, nil
}

func (p *PGDump) listing(ctx context.Context) ([]backup.Info, bool, error) {
	out, err := p.run(ctx, "list pgdump files", "sh", "-eu", "-c", listScript, "pgdump", p.directory())
	if err != nil {
		return nil, false, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "missing" {
		return nil, false, nil
	}
	if lines[0] != "exists" {
		return nil, false, fmt.Errorf("read pgdump directory: invalid listing header\nTry: inspect the backups/pgdump directory on the VM")
	}
	var files []backup.Info
	for _, line := range lines[1:] {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, false, fmt.Errorf("read pgdump file: invalid stat record %q\nTry: inspect the backups/pgdump directory on the VM", line)
		}
		label := strings.TrimSuffix(path.Base(fields[0]), ".dump")
		start, err := labelTime(label)
		if err != nil {
			return nil, false, err
		}
		size, sizeErr := strconv.ParseInt(fields[1], 10, 64)
		mtime, timeErr := strconv.ParseInt(fields[2], 10, 64)
		if sizeErr != nil || timeErr != nil || size < 0 || fields[0] != path.Join(p.directory(), label+".dump") {
			return nil, false, fmt.Errorf("read pgdump metadata: invalid stat record %q\nTry: inspect the dump with stat on the VM", line)
		}
		files = append(files, backup.Info{Label: label, Kind: backup.Full, Start: start, Stop: time.Unix(mtime, 0).UTC(), Size: size, RepoSize: size})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Stop.Equal(files[j].Stop) {
			return files[i].Label < files[j].Label
		}
		return files[i].Stop.Before(files[j].Stop)
	})
	return files, true, nil
}

// List returns complete dumps in order of modification time, oldest first.
func (p *PGDump) List(ctx context.Context) ([]backup.Info, error) {
	files, _, err := p.listing(ctx)
	return files, err
}

// Info reports the directory status, latest dump, and pg_dump version.
func (p *PGDump) Info(ctx context.Context) (backup.Status, error) {
	files, exists, err := p.listing(ctx)
	if err != nil {
		return backup.Status{}, err
	}
	version, err := p.run(ctx, "read pg_dump version", p.postgres("pg_dump", "--version")...)
	if err != nil {
		return backup.Status{}, err
	}
	s := backup.Status{Stanza: p.Env.App, Exists: exists, Backups: files, Version: strings.TrimSpace(string(version))}
	if len(files) > 0 {
		s.Last = &s.Backups[len(files)-1]
	}
	return s, nil
}

// Expire deletes older dumps while retaining at least the newest dump.
func (p *PGDump) Expire(ctx context.Context) error {
	if p.Retention < 1 {
		return fmt.Errorf("expire pgdump backups: retention must be positive\nTry: set [backups] retention to \"7\"")
	}
	files, err := p.List(ctx)
	if err != nil {
		return err
	}
	if len(files) <= p.Retention {
		return nil
	}
	args := []string{"rm", "-f", "--"}
	for _, f := range files[:len(files)-p.Retention] {
		args = append(args, path.Join(p.directory(), f.Label+".dump"))
	}
	_, err = p.run(ctx, "expire pgdump backups", args...)
	return err
}
