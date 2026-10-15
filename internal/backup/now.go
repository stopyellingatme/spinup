package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// Options selects the project, the environment, and the output of a backup command.
type Options struct {
	// Dir is the project directory. Empty means the current directory.
	Dir string
	// Env names the environment in spinup.toml.
	Env string
	// Kind is the backup type of Now. Empty means Diff.
	Kind Kind
	// JSON writes one JSON document to Out.
	JSON bool
	// Out receives the result. ErrOut receives the progress of pgBackRest.
	Out, ErrOut io.Writer
	// Remote replaces the SSH runner. Tests use remote.Fake.
	Remote remote.Runner
	// Runner runs local commands for the SSH runner. Nil means the operating system runner.
	Runner runexec.Runner
}

func writers(o Options) (io.Writer, io.Writer) {
	out, errOut := o.Out, o.ErrOut
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	return out, errOut
}

// ReadConfig reads the rendered pgBackRest configuration of the project at root.
// It fails when the project does not use the backups module.
func ReadConfig(root string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ConfigPath)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("back up the database: the project does not use the backups module (%s is missing)\nTry: add the backups module to the project", ConfigPath)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w\nTry: check the file permissions", ConfigPath, err)
	}
	return data, nil
}

// Retention returns the number of full backups to keep from spinup.toml [backups] retention.
// Zero means the value of pgbackrest.conf.
func Retention(config *project.Config) (int, error) {
	value := strings.TrimSpace(config.Backups.Retention)
	if value == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 999 {
		return 0, fmt.Errorf("read spinup.toml [backups] retention: %q is not a number of full backups from 1 to 999\nTry: set retention = \"4\" in [backups]", value)
	}
	return n, nil
}

// Now takes one backup in the environment and prints its label, size, and duration.
func Now(ctx context.Context, o Options) (Result, error) {
	kind := o.Kind
	if kind == "" {
		kind = Diff
	}
	if _, err := ParseKind(string(kind)); err != nil {
		return Result{}, err
	}
	backend, s, err := Open(o)
	if err != nil {
		return Result{}, err
	}
	defer s.Close()
	out, _ := writers(o)
	result, err := backend.Backup(ctx, kind)
	if err != nil {
		return Result{}, err
	}
	if o.JSON {
		return result, writeJSON(out, result)
	}
	_, err = fmt.Fprintf(out, "backup %s (%s) of %s: size %s, stored %s, took %s\n", result.Label, result.Kind, s.Environment.Name, HumanSize(result.Size), HumanSize(result.RepoSize), result.Duration.Round(time.Second))
	return result, err
}

// List prints the backups of the environment, oldest first.
func List(ctx context.Context, o Options) ([]Info, error) {
	backend, s, err := Open(o)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	out, _ := writers(o)
	backups, err := backend.List(ctx)
	if err != nil {
		return nil, err
	}
	if o.JSON {
		if backups == nil {
			backups = []Info{}
		}
		return backups, writeJSON(out, backups)
	}
	if len(backups) == 0 {
		_, err = fmt.Fprintf(out, "no backups in %s\n", s.Environment.Name)
		return backups, err
	}
	for _, b := range backups {
		if _, err := fmt.Fprintf(out, "%s  %-4s  %s  size %s  stored %s\n", b.Label, b.Kind, b.Stop.Format(time.RFC3339), HumanSize(b.Size), HumanSize(b.RepoSize)); err != nil {
			return backups, err
		}
	}
	return backups, nil
}

// ShowStatus prints the repository status of the environment.
func ShowStatus(ctx context.Context, o Options) (Status, error) {
	backend, s, err := Open(o)
	if err != nil {
		return Status{}, err
	}
	defer s.Close()
	out, _ := writers(o)
	status, err := backend.Info(ctx)
	if err != nil {
		return Status{}, err
	}
	if o.JSON {
		return status, writeJSON(out, status)
	}
	lines := []string{statusLine(status)}
	if status.Cipher != "" {
		lines = append(lines, "cipher: "+status.Cipher)
	}
	if status.Last != nil {
		lines = append(lines, fmt.Sprintf("last backup: %s (%s), finished %s", status.Last.Label, status.Last.Kind, status.Last.Stop.Format(time.RFC3339)))
	} else {
		lines = append(lines, "last backup: none")
	}
	lines = append(lines, fmt.Sprintf("backups: %d", len(status.Backups)))
	if status.WALMax != "" {
		lines = append(lines, fmt.Sprintf("WAL archive: %s to %s", status.WALMin, status.WALMax))
	}
	if !status.Exists {
		lines = append(lines, "Try: run spinup backup now --env "+s.Environment.Name+" to create the stanza")
	}
	_, err = fmt.Fprintln(out, strings.Join(lines, "\n"))
	return status, err
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
