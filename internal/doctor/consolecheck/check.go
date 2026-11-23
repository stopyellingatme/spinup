// Package consolecheck registers checks for console wiring and readiness.
package consolecheck

import (
	"context"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/modules"
	"github.com/stopyellingatme/spinup/internal/project"
)

func init() {
	doctor.Register(doctor.Check{ID: "console-wired", Title: "Console wiring", Scope: doctor.Local, Quick: true, Run: localCheck})
	doctor.Register(doctor.Check{ID: "console-ready", Title: "Console readiness", Scope: doctor.Remote, Run: remoteCheck})
}

func disabled(c *doctor.Context) bool {
	if c.Root == "" {
		return true
	}
	cfg, err := project.Load(c.Root)
	return err == nil && !modules.Enabled(cfg, "console")
}

func localCheck(_ context.Context, c *doctor.Context) doctor.Result {
	if disabled(c) {
		return doctor.Result{Status: doctor.Skip, Message: "Console module is disabled."}
	}
	return doctor.Result{Status: doctor.Fail, Message: "Console wiring is unsafe or incomplete: api/cmd/console/main.go.", Try: "review the console pack and restore or merge api/cmd/console/main.go"}
}

func remoteCheck(_ context.Context, c *doctor.Context) doctor.Result {
	if disabled(c) {
		return doctor.Result{Status: doctor.Skip, Message: "Console module is disabled."}
	}
	return doctor.Result{Status: doctor.Fail, Message: "Console is unreachable, exposed, or incompatible.", Try: "spinup deploy --resume --env " + c.Env}
}
