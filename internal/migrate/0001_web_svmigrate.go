package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

// SvMigrateID is the ID of the codemod that runs `sv migrate` on a SvelteKit major bump.
const SvMigrateID = "0001_web_svmigrate"

// svMigrateTimeout bounds one `pnpm dlx sv migrate` run, which may download sv.
const svMigrateTimeout = 10 * time.Minute

func init() { Register(svMigrate(stepStacks)) }

// stepStacks returns the stack versions of the old and new templates of the step.
// It returns nil stacks when either version is unknown.
func stepStacks(c *Ctx) (old, next *stackversions.Stack) {
	if c.Old == nil || c.New == nil {
		return nil, nil
	}
	return c.Old.Stack, c.New.Stack
}

// svStep is a planned `sv migrate` run.
type svStep struct {
	from, to  string // the old and new @sveltejs/kit pins
	major     int    // the new SvelteKit major version
	svVersion string // the pinned sv CLI version
}

// args returns the command line in web/.
func (s svStep) args() []string {
	return []string{"dlx", "sv@" + s.svVersion, "migrate", "sveltekit-" + strconv.Itoa(s.major)}
}

// svMigrate returns the After codemod. stacks gives the old and new stack versions of a step.
// When the step moves @sveltejs/kit to a new major version, the codemod runs
// `pnpm dlx sv@<pin> migrate sveltekit-<major>` in web/. sv migrations change only code
// that still uses the old API, so a second run on a migrated tree changes nothing.
func svMigrate(stacks func(*Ctx) (old, next *stackversions.Stack)) Migration {
	plan := func(c *Ctx) (*svStep, error) {
		old, next := stacks(c)
		if old == nil || next == nil {
			return nil, nil
		}
		if _, err := os.Stat(filepath.Join(c.Root, "web", "package.json")); errors.Is(err, os.ErrNotExist) {
			return nil, nil // An API-only app has no web/ to migrate.
		} else if err != nil {
			return nil, fmt.Errorf("check web/package.json: %w\nTry: make the web directory readable", err)
		}
		from, ok := old.NPM["@sveltejs/kit"]
		if !ok {
			return nil, nil // The old version had no web pack.
		}
		to, err := next.NPMVersion("@sveltejs/kit")
		if err != nil {
			return nil, fmt.Errorf("read the new @sveltejs/kit pin: %w\nTry: use a template version with a complete stackversions.yaml", err)
		}
		fromMajor, err := npmMajor(from)
		if err != nil {
			return nil, err
		}
		toMajor, err := npmMajor(to)
		if err != nil {
			return nil, err
		}
		if toMajor <= fromMajor {
			return nil, nil
		}
		sv, err := next.NPMVersion("sv")
		if err != nil {
			return nil, fmt.Errorf("read the sv pin: %w\nTry: use a template version that pins sv in stackversions.yaml", err)
		}
		return &svStep{from: from, to: to, major: toMajor, svVersion: sv}, nil
	}
	return Migration{
		ID:    SvMigrateID,
		Since: "0.2.0",
		Desc:  "Run sv migrate when SvelteKit moves to a new major version",
		Stage: After,
		Plan: func(c *Ctx) ([]Change, error) {
			step, err := plan(c)
			if err != nil || step == nil {
				return nil, err
			}
			return []Change{{
				Path: "web",
				Desc: fmt.Sprintf("SvelteKit %s to %s: run pnpm %s", step.from, step.to, strings.Join(step.args(), " ")),
			}}, nil
		},
		Apply: func(c *Ctx) error {
			step, err := plan(c)
			if err != nil || step == nil {
				return err
			}
			if c.Runner == nil {
				return errors.New("run sv migrate: no command runner\nTry: run spinup upgrade --continue")
			}
			cmd := exec.Cmd{Name: "pnpm", Args: step.args(), Dir: filepath.Join(c.Root, "web"), Timeout: svMigrateTimeout}
			if _, err := c.Runner.Run(context.Background(), cmd); err != nil {
				return fmt.Errorf("run %s in web/: %w\nTry: run the command by hand, then run spinup upgrade --continue", cmd.String(), err)
			}
			return nil
		},
	}
}

// npmMajor returns the major version of an exact npm pin such as "3.0.1".
func npmMajor(pin string) (int, error) {
	head, _, _ := strings.Cut(strings.TrimLeft(pin, "^~v="), ".")
	major, err := strconv.Atoi(head)
	if err != nil || major < 0 {
		return 0, fmt.Errorf("parse npm version %q: not a semantic version\nTry: pin @sveltejs/kit to an exact version in stackversions.yaml", pin)
	}
	return major, nil
}
