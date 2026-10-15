package ops

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// versionSQL reads the server version as a number, for example 170006.
const versionSQL = "SELECT current_setting('server_version_num')"

// Version support of PostgreSQL majors (architecture §12).
const (
	pgOldestAllowed = 14 // 13 and older fail
	pgEOLWarn       = 14 // end of life on 2026-11-12
	pgNewestAllowed = 18 // 19 and newer are beta and fail
)

// VersionCheck is the verdict on one PostgreSQL major version.
type VersionCheck struct {
	// Major is the major version, for example 17.
	Major int
	// Warn is a warning to show. It is empty when there is none.
	Warn string
	// Fail is the reason the version is not allowed. It is empty when the version is allowed.
	Fail string
}

// CheckServerVersion parses server_version_num and checks the major version.
func CheckServerVersion(num string) (VersionCheck, error) {
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil || n < 10000 {
		return VersionCheck{}, fmt.Errorf("%q is not a server_version_num", strings.TrimSpace(num))
	}
	c := VersionCheck{Major: n / 10000}
	switch {
	case c.Major < pgOldestAllowed:
		c.Fail = fmt.Sprintf("PostgreSQL %d is past its end of life and is not supported", c.Major)
	case c.Major > pgNewestAllowed:
		c.Fail = fmt.Sprintf("PostgreSQL %d is a beta and is not allowed", c.Major)
	case c.Major == pgEOLWarn:
		c.Warn = fmt.Sprintf("PostgreSQL %d reaches its end of life on 2026-11-12", c.Major)
	}
	return c, nil
}

// Doctor checks the server version of the development database and reports
// the pending migrations. A version that is not allowed gives an error.
func Doctor(ctx context.Context, o Options, runner run.Runner) error {
	const what = "Database check failed"
	root, config, err := loadProject(o, what)
	if err != nil {
		return err
	}
	if err := guardLocal(o, config, what, "db doctor", false); err != nil {
		return err
	}
	c := composeExec(root, false, psqlArgs("-At", "-c", versionSQL)...)
	if o.DryRun {
		if err := printCmd(o.Out, c, ""); err != nil {
			return err
		}
		return Status(ctx, o, runner)
	}
	c.Stderr = o.ErrOut
	res, err := runner.Run(ctx, c)
	if err != nil {
		return opError(what, err)
	}
	check, err := CheckServerVersion(string(res.Stdout))
	if err != nil {
		return opError(what, err)
	}
	if check.Fail == "" {
		_, _ = fmt.Fprintf(o.Out, "ok  PostgreSQL %d\n", check.Major)
	}
	if check.Warn != "" {
		_, _ = fmt.Fprintf(o.ErrOut, "Warning: %s.\n  Try: upgrade to PostgreSQL %d before that date\n", check.Warn, pgNewestAllowed)
	}

	var status bytes.Buffer
	so := o
	so.Out = &status
	if err := Status(ctx, so, runner); err != nil {
		return err
	}
	var pending []string
	sc := bufio.NewScanner(&status)
	for sc.Scan() {
		if name, ok := strings.CutPrefix(sc.Text(), "pending  "); ok {
			pending = append(pending, name)
		}
	}
	if len(pending) == 0 {
		_, _ = fmt.Fprintln(o.Out, "ok  no pending migrations")
	} else {
		_, _ = fmt.Fprintf(o.ErrOut, "Warning: %d pending migrations: %s.\n  Try: spinup db migrate up\n", len(pending), strings.Join(pending, ", "))
	}
	if check.Fail != "" {
		return &ui.Error{What: what, Why: check.Fail, Try: fmt.Sprintf("run PostgreSQL %d to %d (the postgres image in %s)", pgOldestAllowed+1, pgNewestAllowed, ComposeFile)}
	}
	return nil
}
