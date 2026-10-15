package db

import (
	"context"
	"fmt"
	"io/fs"
	"os"
)

// MigrateCommand returns the app command "migrate". The generated main.go
// registers it, so `go run ./cmd/api migrate <up|down|status>` runs the
// migrations in fsys.
//
// The database URL comes from [WithDatabaseURL], else from DATABASE_URL.
// The output has no durations, so scripts can compare it:
//
//	up:     "migrate: no pending migrations", or "migrate: N pending" and "applied  <file>" lines
//	down:   "rolled back  <file>", or "migrate: nothing to roll back"
//	status: one "applied  <file>" or "pending  <file>" line for each file, in version order
func MigrateCommand(fsys fs.FS, opts ...MigrateOption) func(ctx context.Context, args []string) error {
	return func(ctx context.Context, args []string) error {
		sub := ""
		if len(args) > 0 {
			sub = args[0]
		}
		switch sub {
		case "up", "down", "status":
		default:
			why := "no subcommand given"
			if sub != "" {
				why = fmt.Sprintf("unknown subcommand %q", sub)
			}
			return &userError{
				what: "migrate",
				why:  why + " (want up, down, or status)",
				try:  "migrate up | migrate down | migrate status",
			}
		}
		if len(args) > 1 {
			return &userError{
				what: "migrate " + sub,
				why:  fmt.Sprintf("unexpected arguments %q", args[1:]),
				try:  "migrate " + sub,
			}
		}
		cfg := newMigrateConfig(opts)
		url := cfg.url
		if url == "" {
			url = os.Getenv("DATABASE_URL")
		}
		m, err := NewMigrator(url, fsys, opts...)
		if err != nil {
			return err
		}
		defer func() { _ = m.Close() }()
		switch sub {
		case "up":
			return m.Up(ctx)
		case "down":
			return m.Down(ctx)
		default:
			list, err := m.Status(ctx)
			if err != nil {
				return err
			}
			for _, s := range list {
				state := "pending"
				if s.Applied {
					state = "applied"
				}
				m.printf("%s  %s\n", state, s.Name)
			}
			return nil
		}
	}
}
