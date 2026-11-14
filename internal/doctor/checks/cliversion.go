package checks

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

func init() {
	doctor.Register(doctor.Check{ID: "cli-version", Title: "CLI and lock version", Scope: doctor.Local, Quick: true, Run: cliVersion})
}

func cliVersion(_ context.Context, c *doctor.Context) doctor.Result {
	if c.Root == "" {
		return doctor.Result{Status: doctor.Skip, Message: "outside a spinup project"}
	}
	cli, _ := c.Deps["cli_version"].(string)
	if cli == "" || cli == "dev" {
		return doctor.Result{Status: doctor.Skip, Message: "development build of spinup"}
	}
	lock, err := manifest.Load(filepath.Join(c.Root, project.LockFile))
	if err != nil {
		return doctor.Result{Status: doctor.Skip, Message: "spinup.lock cannot be read"}
	}
	advice := compat.CheckPin(cli, lock.CLIVersion)
	switch advice.State {
	case compat.PinUnknown:
		return doctor.Result{Status: doctor.Skip, Message: fmt.Sprintf("spinup.lock records %s, which is not a release version", lock.CLIVersion)}
	case compat.PinMatch:
		return doctor.Result{Status: doctor.OK, Message: fmt.Sprintf("CLI %s, spinup.lock %s", cli, lock.CLIVersion)}
	default:
		return doctor.Result{Status: doctor.Warn, Message: advice.Message, Try: advice.Try}
	}
}
