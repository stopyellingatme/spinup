package walg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// SecretLoader exports WAL-G configuration from the mounted secret files.
// The values remain in the container environment and never enter command arguments.
const SecretLoader = `set -eu; for f in /run/secrets/WALG_* /run/secrets/AWS_*; do [ -f "$f" ] || continue; value=$(cat "$f"); export "${f##*/}=$value"; done; : "${WALG_S3_PREFIX:?WALG_S3_PREFIX is required}" "${WALG_LIBSODIUM_KEY:?WALG_LIBSODIUM_KEY is required}"; `

var (
	appPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	labelPattern = regexp.MustCompile(`^base_([0-9A-F]{24})(?:_D_([0-9A-F]{24}))?$`)
)

func init() { backup.RegisterBackend("walg", New) }

// WALG stores physical backups and WAL through the deployment VM.
type WALG struct {
	// Env supplies the project, remote runner, and progress stream.
	Env backup.Env
	// Retention is the number of full backups to retain.
	Retention int
	// Now supplies the clock for restore ownership and elapsed time.
	Now func() time.Time
}

var _ backup.Backend = (*WALG)(nil)

// New constructs the registered WAL-G backend.
func New(e backup.Env) (backup.Backend, error) {
	if !appPattern.MatchString(e.App) || e.Remote == nil {
		return nil, fmt.Errorf("initialize WAL-G: a valid app name and remote runner are required\nTry: select a configured deployment environment")
	}
	retention := 4
	if e.Config != nil && e.Config.Backups.Retention != "" {
		n, err := strconv.Atoi(e.Config.Backups.Retention)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("initialize WAL-G: retention must be a positive integer\nTry: set [backups] retention to \"4\"")
		}
		retention = n
	}
	return &WALG{Env: e, Retention: retention, Now: time.Now}, nil
}

func (w *WALG) run(ctx context.Context, what string, args ...string) ([]byte, error) {
	progress := w.Env.Progress
	if progress == nil {
		progress = io.Discard
	}
	result, err := w.Env.Remote.Run(ctx, remote.Cmd{Argv: args, Stderr: progress, Timeout: 30 * time.Minute})
	if err != nil {
		return nil, fmt.Errorf("%s on the VM: %w\nTry: inspect the postgres logs and WAL-G secrets", what, err)
	}
	return result.Stdout, nil
}

func (w *WALG) compose(args ...string) []string {
	dir := "/opt/" + w.Env.App + "/current"
	return append([]string{"docker", "compose", "-p", w.Env.App, "--project-directory", dir, "-f", dir + "/compose.edge.yml", "-f", dir + "/compose.backups-walg.yml"}, args...)
}

func (w *WALG) command(ctx context.Context, args ...string) ([]byte, error) {
	argv := w.compose("exec", "-T", "postgres", "sh", "-c", SecretLoader+`exec wal-g "$@"`, "wal-g")
	return w.run(ctx, "run WAL-G", append(argv, args...)...)
}

// Backup creates a full backup, or a delta when WAL-G delta backups are configured.
// Full explicitly overrides WALG_DELTA_MAX_STEPS. Diff and Incr use that setting.
func (w *WALG) Backup(ctx context.Context, kind backup.Kind) (backup.Result, error) {
	if _, err := backup.ParseKind(string(kind)); err != nil || kind == "" {
		return backup.Result{}, fmt.Errorf("create WAL-G backup: invalid kind %q\nTry: use full, diff, or incr", kind)
	}
	script := SecretLoader
	if kind == backup.Full {
		script += `export WALG_DELTA_MAX_STEPS=0; `
	}
	script += `exec wal-g backup-push "$PGDATA"`
	if _, err := w.run(ctx, "create WAL-G backup", w.compose("exec", "-T", "postgres", "sh", "-c", script)...); err != nil {
		return backup.Result{}, err
	}
	files, err := w.List(ctx)
	if err != nil {
		return backup.Result{}, err
	}
	if len(files) == 0 {
		return backup.Result{}, fmt.Errorf("read the new WAL-G backup: the repository is empty\nTry: inspect wal-g backup-list --json --detail on the VM")
	}
	b := files[len(files)-1]
	return backup.Result{Label: b.Label, Kind: b.Kind, Start: b.Start, Stop: b.Stop, Duration: b.Stop.Sub(b.Start), Size: b.Size, RepoSize: b.RepoSize}, nil
}

type backupJSON struct {
	Name       string    `json:"backup_name"`
	Time       time.Time `json:"time"`
	Start      time.Time `json:"start_time"`
	Finish     time.Time `json:"finish_time"`
	Size       int64     `json:"uncompressed_size"`
	Compressed int64     `json:"compressed_size"`
}

func parseList(data []byte) ([]backup.Info, error) {
	var entries []backupJSON
	err := json.Unmarshal(data, &entries)
	if err != nil {
		return nil, fmt.Errorf("parse WAL-G backup list: %w\nTry: check wal-g backup-list --json --detail on the VM", err)
	}
	if entries == nil {
		return nil, fmt.Errorf("parse WAL-G backup list: expected a JSON array\nTry: check wal-g backup-list --json --detail on the VM")
	}
	out := make([]backup.Info, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		parts := labelPattern.FindStringSubmatch(e.Name)
		if e.Start.IsZero() {
			e.Start = e.Time
		}
		if e.Finish.IsZero() {
			e.Finish = e.Time
		}
		if len(parts) == 0 || seen[e.Name] || e.Start.IsZero() || e.Finish.IsZero() || e.Finish.Before(e.Start) || e.Size < 0 || e.Compressed < 0 {
			return nil, fmt.Errorf("parse WAL-G backup metadata: invalid entry %q\nTry: inspect wal-g backup-list --json --detail on the VM", e.Name)
		}
		seen[e.Name] = true
		info := backup.Info{Label: e.Name, Kind: backup.Full, Start: e.Start, Stop: e.Finish, Size: e.Size, RepoSize: e.Compressed}
		if parts[2] != "" {
			info.Kind = backup.Incr
			info.Prior = "base_" + parts[2]
		}
		out = append(out, info)
	}
	// A delta can depend on another delta. Resolve its complete repository label.
	labels := make(map[string]string, len(out))
	for _, b := range out {
		labels[labelPattern.FindStringSubmatch(b.Label)[1]] = b.Label
	}
	for i := range out {
		if prior := strings.TrimPrefix(out[i].Prior, "base_"); prior != "" {
			if label, ok := labels[prior]; ok {
				out[i].Prior = label
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Stop.Equal(out[j].Stop) {
			return out[i].Label < out[j].Label
		}
		return out[i].Stop.Before(out[j].Stop)
	})
	return out, nil
}

// List returns WAL-G backups, oldest first, from its detailed JSON listing.
func (w *WALG) List(ctx context.Context) ([]backup.Info, error) {
	out, err := w.command(ctx, "backup-list", "--json", "--detail")
	if err != nil {
		return nil, err
	}
	return parseList(out)
}

// Info reports the repository contents and its newest backup.
func (w *WALG) Info(ctx context.Context) (backup.Status, error) {
	files, err := w.List(ctx)
	if err != nil {
		return backup.Status{}, err
	}
	s := backup.Status{Stanza: w.Env.App, Exists: len(files) > 0, Cipher: "libsodium", Backups: files}
	if len(files) > 0 {
		s.Last = &s.Backups[len(files)-1]
	}
	return s, nil
}

// Expire applies retention to full backups and their dependent delta backups.
func (w *WALG) Expire(ctx context.Context) error {
	if w.Retention < 1 {
		return fmt.Errorf("expire WAL-G backups: retention must be positive\nTry: set [backups] retention to \"4\"")
	}
	_, err := w.command(ctx, "delete", "retain", "FULL", strconv.Itoa(w.Retention), "--confirm")
	return err
}

func (w *WALG) target(ctx context.Context, t backup.Target) (backup.Info, error) {
	if t.Label != "" && (!t.Time.IsZero() || !labelPattern.MatchString(t.Label)) {
		return backup.Info{}, fmt.Errorf("select WAL-G target: invalid label or conflicting time\nTry: select one label from spinup backup list, or a recovery time")
	}
	files, err := w.List(ctx)
	if err != nil {
		return backup.Info{}, err
	}
	for i := len(files) - 1; i >= 0; i-- {
		b := files[i]
		if t.Label != "" {
			if t.Label == b.Label {
				return b, nil
			}
			continue
		}
		if t.Time.IsZero() || !b.Stop.After(t.Time) {
			return b, nil
		}
	}
	return backup.Info{}, fmt.Errorf("select WAL-G backup: no backup matches the target\nTry: select a label or a time after a completed backup")
}

// Verify restores a backup in an isolated container and checks goose_db_version.
func (w *WALG) Verify(ctx context.Context, t backup.Target) (backup.VerifyResult, error) {
	b, err := w.target(ctx, t)
	if err != nil {
		return backup.VerifyResult{}, err
	}
	return w.restoredContainer(ctx, b, t, false)
}

// Restore restores into a separate container or the stopped live database volume.
// The caller supplies the deploy lock, confirmation, and application shutdown.
func (w *WALG) Restore(ctx context.Context, t backup.Target, into backup.Into) error {
	if into.New == into.InPlace {
		return fmt.Errorf("restore WAL-G: select exactly one destination\nTry: pass --into new or --into in-place")
	}
	b, err := w.target(ctx, t)
	if err != nil {
		return err
	}
	if into.New {
		_, err = w.restoredContainer(ctx, b, t, true)
		return err
	}
	return w.inPlace(ctx, b, t)
}

func text(out []byte) string { return strings.TrimSpace(string(out)) }
