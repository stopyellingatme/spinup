package db

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strconv"
)

// migrateSubs lists the subcommands of [MigrateCommand] and their argument counts.
var migrateSubs = map[string]int{"up": 0, "down": 0, "status": 0, "redo": 0, "to": 1, "version": 0}

const migrateUsage = "migrate up | down | status | redo | to <version> | version"

// MigrateCommand returns the app command "migrate". The generated main.go
// registers it, so `go run ./cmd/api migrate <subcommand>` runs the
// migrations in fsys.
//
// The database URL comes from [WithDatabaseURL], else from DATABASE_URL.
// The output has no durations, so scripts can compare it:
//
//	up:      "migrate: no pending migrations", or "migrate: N pending" and "applied  <file>" lines
//	down:    "rolled back  <file>", or "migrate: nothing to roll back"
//	status:  one "applied  <file>" or "pending  <file>" line for each file, in version order
//	redo:    "rolled back  <file>" and "applied  <file>", or "migrate: nothing to redo"
//	to <v>:  "applied  <file>" or "rolled back  <file>" lines, or "migrate: already at version <v>"
//	version: the highest applied version number, 0 when none is applied
//
// Hooks from [WithHook] run in the order given. Every AfterUp runs after a
// successful up, after redo, and after a to that moves up. Every BeforeDown
// runs before down, before redo, and before a to that moves down.
func MigrateCommand(fsys fs.FS, opts ...MigrateOption) func(ctx context.Context, args []string) error {
	return func(ctx context.Context, args []string) error {
		sub := ""
		if len(args) > 0 {
			sub = args[0]
		}
		want, ok := migrateSubs[sub]
		if !ok {
			why := "no subcommand given"
			if sub != "" {
				why = fmt.Sprintf("unknown subcommand %q", sub)
			}
			return &userError{
				what: "migrate",
				why:  why + " (want up, down, status, redo, to, or version)",
				try:  migrateUsage,
			}
		}
		if len(args)-1 != want {
			why := fmt.Sprintf("unexpected arguments %q", args[1:])
			try := "migrate " + sub
			if sub == "to" {
				why = "to needs exactly one version number"
				try = "migrate to 1"
			}
			return &userError{what: "migrate " + sub, why: why, try: try}
		}
		var target int64
		if sub == "to" {
			v, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil || v < 0 {
				return &userError{
					what: "migrate to",
					why:  fmt.Sprintf("%q is not a migration version (want 0 or a positive number)", args[1]),
					try:  "migrate to 1",
				}
			}
			target = v
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
		h := hookRunner{hooks: cfg.hooks, url: url, sub: sub}
		switch sub {
		case "up":
			if err := m.Up(ctx); err != nil {
				return err
			}
			return h.afterUp(ctx)
		case "down":
			if err := h.beforeDown(ctx); err != nil {
				return err
			}
			return m.Down(ctx)
		case "redo":
			if err := h.beforeDown(ctx); err != nil {
				return err
			}
			if err := m.Redo(ctx); err != nil {
				return err
			}
			return h.afterUp(ctx)
		case "to":
			return migrateTo(ctx, m, h, target)
		case "version":
			v, err := m.Version(ctx)
			if err != nil {
				return err
			}
			m.printf("%d\n", v)
			return nil
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

// migrateTo moves the database up or down to the version target.
func migrateTo(ctx context.Context, m *Migrator, h hookRunner, target int64) error {
	if target != 0 && !m.HasVersion(target) {
		return &userError{
			what: "migrate to",
			why:  fmt.Sprintf("no migration file has version %d", target),
			try:  "migrate status (it lists the migration files), then migrate to <version>",
		}
	}
	current, err := m.Version(ctx)
	if err != nil {
		return err
	}
	switch {
	case target > current:
		if err := m.UpTo(ctx, target); err != nil {
			return err
		}
		return h.afterUp(ctx)
	case target < current:
		if err := h.beforeDown(ctx); err != nil {
			return err
		}
		return m.DownTo(ctx, target)
	default:
		m.printf("migrate: already at version %d\n", target)
		return nil
	}
}

// hookRunner runs the hooks of one migrate command.
type hookRunner struct {
	hooks []Hook
	url   string
	sub   string
}

func (h hookRunner) afterUp(ctx context.Context) error {
	for i, hook := range h.hooks {
		if hook.AfterUp == nil {
			continue
		}
		if err := hook.AfterUp(ctx, h.url); err != nil {
			return h.fail("AfterUp", i, err)
		}
	}
	return nil
}

func (h hookRunner) beforeDown(ctx context.Context) error {
	for i, hook := range h.hooks {
		if hook.BeforeDown == nil {
			continue
		}
		if err := hook.BeforeDown(ctx, h.url); err != nil {
			return h.fail("BeforeDown", i, err)
		}
	}
	return nil
}

func (h hookRunner) fail(kind string, i int, err error) error {
	return &userError{
		what: "migrate " + h.sub,
		why:  fmt.Sprintf("%s of migration hook %d of %d failed: %v", kind, i+1, len(h.hooks), err),
		try:  "fix the cause above, then run migrate " + h.sub + " again",
		err:  err,
	}
}
