package backup

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Kind is the type of a physical backup.
type Kind string

// The backup kinds. A diff or incr backup without a full backup becomes a full backup.
const (
	// Full copies the whole database cluster.
	Full Kind = "full"
	// Diff copies the changes since the last full backup.
	Diff Kind = "diff"
	// Incr copies the changes since the last backup of any kind.
	Incr Kind = "incr"
)

// ParseKind returns the Kind for name. An empty name gives Diff.
func ParseKind(name string) (Kind, error) {
	switch Kind(name) {
	case "":
		return Diff, nil
	case Full, Diff, Incr:
		return Kind(name), nil
	}
	return "", fmt.Errorf("invalid backup type %q: the type must be full, diff, or incr\nTry: pass --type full, --type diff, or --type incr", name)
}

// Result describes one completed backup.
type Result struct {
	Label    string        `json:"label"`
	Kind     Kind          `json:"type"`
	Start    time.Time     `json:"start"`
	Stop     time.Time     `json:"stop"`
	Duration time.Duration `json:"duration_ns"`
	// Size is the size of the database at backup time, in bytes.
	Size int64 `json:"size"`
	// RepoSize is the stored (compressed) size of the backup in the repository, in bytes.
	RepoSize int64 `json:"repo_size"`
}

// Info describes one backup in the repository.
type Info struct {
	Label    string    `json:"label"`
	Kind     Kind      `json:"type"`
	Start    time.Time `json:"start"`
	Stop     time.Time `json:"stop"`
	Size     int64     `json:"size"`
	RepoSize int64     `json:"repo_size"`
	// Prior is the label of the backup that this backup depends on. It is empty for a full backup.
	Prior string `json:"prior,omitempty"`
	Error bool   `json:"error"`
}

// Target selects a backup or a point in time. A zero Target selects the latest backup.
type Target struct {
	Label string    `json:"label,omitempty"`
	Time  time.Time `json:"time,omitzero"`
}

// Into selects where a restore writes the database.
type Into struct {
	// New restores into a new, separate container.
	New bool
	// InPlace replaces the live database.
	InPlace bool
}

// VerifyResult describes a restore test.
type VerifyResult struct {
	Label string        `json:"label"`
	RTO   time.Duration `json:"rto_ns"`
	OK    bool          `json:"ok"`
}

// Status summarizes the backup repository.
type Status struct {
	Stanza string `json:"stanza"`
	// Code is the backend status code. Zero means OK.
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Exists is false when the repository has no stanza yet.
	Exists  bool   `json:"exists"`
	Cipher  string `json:"cipher"`
	Backups []Info `json:"backups"`
	// Last is the newest backup. It is nil when the repository has no backup.
	Last *Info `json:"last,omitempty"`
	// WALMin and WALMax are the oldest and newest archived WAL segments.
	WALMin string `json:"wal_min,omitempty"`
	WALMax string `json:"wal_max,omitempty"`
	// Version is the backend version on the server.
	Version string `json:"version,omitempty"`
}

// Backend stores and restores physical database backups.
// pgBackRest is the default backend (ADR 0004). Later backends implement the same interface.
type Backend interface {
	// Backup takes one backup and returns the new backup.
	Backup(ctx context.Context, kind Kind) (Result, error)
	// List returns the backups in the repository, oldest first.
	List(ctx context.Context) ([]Info, error)
	// Verify restores target into a throwaway database and checks it.
	Verify(ctx context.Context, target Target) (VerifyResult, error)
	// Restore restores target into the selected destination.
	Restore(ctx context.Context, target Target, into Into) error
	// Expire removes backups that the retention policy no longer keeps.
	Expire(ctx context.Context) error
	// Info returns the repository status.
	Info(ctx context.Context) (Status, error)
}

// ErrNotImplemented marks a backend operation that a later milestone adds.
var ErrNotImplemented = errors.New("not in this spinup version")

func notInM5(operation string) error {
	return fmt.Errorf("backup %s: %w (milestone M7 adds it)\nTry: use spinup backup now, list, or status", operation, ErrNotImplemented)
}
