package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/remote"
)

// ConfigPath is the path of the rendered pgBackRest configuration in a project.
const ConfigPath = "deploy/pgbackrest/pgbackrest.conf"

// SecretLoader is the shell prefix that exports each /run/secrets/PGBACKREST_* file as an
// environment variable. pgBackRest reads its repository options and keys from these
// variables. deploy/compose.backups.yml uses the same loop for the Postgres server, so
// archive-push sees the same values as the commands that spinup runs.
const SecretLoader = `for f in /run/secrets/PGBACKREST_*; do [ -f "$f" ] && export "${f##*/}=$(cat "$f")"; done; `

// Status codes of `pgbackrest info` that mean the stanza does not exist.
const (
	statusMissingStanza     = 1
	statusMissingStanzaData = 3
)

// PGBackRest runs pgBackRest inside the postgres container of the base Compose project on
// the VM. The stanza is the app name.
type PGBackRest struct {
	// App is the app name. It names the Compose project, the stanza, and /opt/<app>.
	App string
	// Remote runs commands on the VM.
	Remote remote.Runner
	// Config is the rendered pgbackrest.conf. Backup uploads it to the VM. Nil skips the upload.
	Config []byte
	// RetentionFull replaces repo1-retention-full in the uploaded configuration when it is
	// above zero. It comes from spinup.toml [backups] retention.
	RetentionFull int
	// Progress receives the log output of pgBackRest. Nil discards it.
	Progress io.Writer
}

var _ Backend = (*PGBackRest)(nil)

// RemoteConfigPath returns the VM path of the pgBackRest configuration.
// compose.backups.yml mounts /opt/<app>/backups at /var/lib/pgbackrest.
func RemoteConfigPath(app string) string { return path.Join("/opt", app, "backups", "pgbackrest.conf") }

// composeExec returns the argv prefix that runs a command in the postgres container.
func composeExec(app string) []string {
	return []string{"docker", "compose", "-p", app, "exec", "-T", "postgres"}
}

// Command returns the remote argv that runs pgbackrest with args for the stanza app.
func Command(app string, args ...string) []string {
	argv := append(composeExec(app), "sh", "-c", SecretLoader+`exec pgbackrest "$@"`, "pgbackrest", "--stanza="+app)
	return append(argv, args...)
}

func (p *PGBackRest) run(ctx context.Context, what string, args ...string) ([]byte, error) {
	if p.Remote == nil {
		return nil, fmt.Errorf("%s: the remote runner is missing\nTry: report this spinup bug", what)
	}
	progress := p.Progress
	if progress == nil {
		progress = io.Discard
	}
	result, err := p.Remote.Run(ctx, remote.Cmd{Argv: Command(p.App, args...), Stderr: progress})
	if err != nil {
		return nil, fmt.Errorf("%s on the VM: %w\nTry: run spinup backup status --env <env>, and check the PGBACKREST_* secrets and deploy/compose.backups.yml", what, err)
	}
	return result.Stdout, nil
}

// Prepare uploads the configuration and creates the stanza when the repository has none.
// It is idempotent. It reports whether it created the stanza.
func (p *PGBackRest) Prepare(ctx context.Context) (bool, error) {
	if p.Config != nil {
		if p.Remote == nil {
			return false, fmt.Errorf("upload pgBackRest configuration: the remote runner is missing\nTry: report this spinup bug")
		}
		if err := p.Remote.Upload(ctx, WithRetention(p.Config, p.RetentionFull), RemoteConfigPath(p.App), 0o600); err != nil {
			return false, fmt.Errorf("upload pgBackRest configuration to %s: %w\nTry: run spinup server bootstrap, which creates /opt/%s/backups", RemoteConfigPath(p.App), err, p.App)
		}
	}
	status, err := p.Info(ctx)
	if err != nil {
		return false, err
	}
	if status.Exists {
		return false, nil
	}
	if _, err := p.run(ctx, "create pgBackRest stanza", "stanza-create"); err != nil {
		return false, err
	}
	return true, nil
}

// Backup prepares the repository, takes one backup, and returns the new backup.
func (p *PGBackRest) Backup(ctx context.Context, kind Kind) (Result, error) {
	if _, err := ParseKind(string(kind)); err != nil || kind == "" {
		return Result{}, fmt.Errorf("invalid backup type %q\nTry: use full, diff, or incr", kind)
	}
	if _, err := p.Prepare(ctx); err != nil {
		return Result{}, err
	}
	if _, err := p.run(ctx, "run pgBackRest "+string(kind)+" backup", "--type="+string(kind), "backup"); err != nil {
		return Result{}, err
	}
	backups, err := p.List(ctx)
	if err != nil {
		return Result{}, err
	}
	if len(backups) == 0 {
		return Result{}, errors.New("read the new backup: pgbackrest info lists no backup\nTry: run spinup backup status --env <env>")
	}
	last := backups[len(backups)-1]
	return Result{Label: last.Label, Kind: last.Kind, Start: last.Start, Stop: last.Stop, Duration: last.Stop.Sub(last.Start), Size: last.Size, RepoSize: last.RepoSize}, nil
}

// WithRetention returns config with repo1-retention-full set to full. A value of zero or less
// returns config unchanged. Without the option, it adds the option after the [global] line.
func WithRetention(config []byte, full int) []byte {
	if full <= 0 {
		return config
	}
	option := "repo1-retention-full=" + strconv.Itoa(full)
	lines := strings.SplitAfter(string(config), "\n")
	global := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "repo1-retention-full=") {
			lines[i] = option + "\n"
			return []byte(strings.Join(lines, ""))
		}
		if trimmed == "[global]" && global < 0 {
			global = i
		}
	}
	if global < 0 {
		return []byte("[global]\n" + option + "\n" + string(config))
	}
	lines = slices.Insert(lines, global+1, option+"\n")
	return []byte(strings.Join(lines, ""))
}

// List returns the backups in the repository, oldest first.
func (p *PGBackRest) List(ctx context.Context) ([]Info, error) {
	status, err := p.Info(ctx)
	if err != nil {
		return nil, err
	}
	return status.Backups, nil
}

// Info runs `pgbackrest info --output=json` and parses the stanza status.
func (p *PGBackRest) Info(ctx context.Context) (Status, error) {
	out, err := p.run(ctx, "read pgBackRest info", "--output=json", "info")
	if err != nil {
		return Status{}, err
	}
	return ParseInfo(p.App, out)
}

// Verify is not in M5. Milestone M7 adds it.
func (p *PGBackRest) Verify(context.Context, Target) (VerifyResult, error) {
	return VerifyResult{}, notInM5("verify")
}

// Restore is not in M5. Milestone M7 adds it.
func (p *PGBackRest) Restore(context.Context, Target, Into) error { return notInM5("restore") }

// Expire is not in M5. Milestone M7 adds it. Each backup already expires old backups with
// the retention of pgbackrest.conf.
func (p *PGBackRest) Expire(context.Context) error { return notInM5("expire") }

type infoJSON struct {
	Name   string `json:"name"`
	Cipher string `json:"cipher"`
	Status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
	Archive []struct {
		Min string `json:"min"`
		Max string `json:"max"`
	} `json:"archive"`
	Backup []struct {
		Label    string  `json:"label"`
		Type     string  `json:"type"`
		Prior    *string `json:"prior"`
		Error    bool    `json:"error"`
		Backrest struct {
			Version string `json:"version"`
		} `json:"backrest"`
		Info struct {
			Size       int64 `json:"size"`
			Repository struct {
				Size  int64 `json:"size"`
				Delta int64 `json:"delta"`
			} `json:"repository"`
		} `json:"info"`
		Timestamp struct {
			Start int64 `json:"start"`
			Stop  int64 `json:"stop"`
		} `json:"timestamp"`
	} `json:"backup"`
}

// ParseInfo parses the output of `pgbackrest info --output=json` for one stanza.
func ParseInfo(stanza string, data []byte) (Status, error) {
	var stanzas []infoJSON
	if err := json.Unmarshal(data, &stanzas); err != nil {
		return Status{}, fmt.Errorf("parse pgbackrest info output: %w\nTry: run pgbackrest info on the VM and check its version", err)
	}
	var found *infoJSON
	for i := range stanzas {
		if stanzas[i].Name == stanza {
			found = &stanzas[i]
		}
	}
	if found == nil {
		return Status{Stanza: stanza, Code: statusMissingStanza, Message: "missing stanza"}, nil
	}
	s := Status{Stanza: stanza, Code: found.Status.Code, Message: found.Status.Message, Cipher: found.Cipher}
	s.Exists = s.Code != statusMissingStanza && s.Code != statusMissingStanzaData
	if len(found.Archive) > 0 {
		s.WALMin, s.WALMax = found.Archive[len(found.Archive)-1].Min, found.Archive[len(found.Archive)-1].Max
	}
	for _, b := range found.Backup {
		info := Info{
			Label: b.Label, Kind: Kind(b.Type), Error: b.Error,
			Start: time.Unix(b.Timestamp.Start, 0).UTC(), Stop: time.Unix(b.Timestamp.Stop, 0).UTC(),
			Size: b.Info.Size, RepoSize: b.Info.Repository.Delta,
		}
		if b.Prior != nil {
			info.Prior = *b.Prior
		}
		s.Backups = append(s.Backups, info)
		s.Version = b.Backrest.Version
	}
	sort.SliceStable(s.Backups, func(i, j int) bool { return s.Backups[i].Start.Before(s.Backups[j].Start) })
	if n := len(s.Backups); n > 0 {
		last := s.Backups[n-1]
		s.Last = &last
	}
	return s, nil
}

// HumanSize formats bytes with binary units.
func HumanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + string("KMGTPE"[exp]) + "iB"
}

// statusLine returns a one-line summary of s.
func statusLine(s Status) string {
	return fmt.Sprintf("stanza %s: %s (code %d)", s.Stanza, s.Message, s.Code)
}
