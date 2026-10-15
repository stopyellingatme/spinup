package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/merge"
	"github.com/stopyellingatme/spinup/internal/project"
)

func init() {
	for _, c := range []doctor.Check{
		{ID: "git-version", Title: "Git version", Scope: doctor.Local, Quick: true, Run: gitVersion},
		{ID: "lock-valid", Title: "Lock validity", Scope: doctor.Local, Run: lockValid},
		{ID: "managed-missing", Title: "Missing managed files", Scope: doctor.Local, Run: managedMissing},
		{ID: "kit-version", Title: "Kit compatibility", Scope: doctor.Local, Run: kitVersion},
	} {
		doctor.Register(c)
	}
}

func skipped(c *doctor.Context) bool { return c.Root == "" || c.Quick }
func skip() doctor.Result {
	return doctor.Result{Status: doctor.Skip, Message: "requires a project and a full check"}
}
func failure(err error, status doctor.Status, try string) doctor.Result {
	message, hint, ok := strings.Cut(err.Error(), "\nTry: ")
	if ok {
		try = hint
	}
	return doctor.Result{Status: status, Message: strings.Join(strings.Fields(message), " "), Try: strings.Join(strings.Fields(try), " ")}
}
func gitVersion(ctx context.Context, c *doctor.Context) doctor.Result {
	runner, ok := c.Deps["runner"].(exec.Runner)
	if !ok {
		return doctor.Result{Status: doctor.Fail, Message: "git check requires an exec.Runner", Try: "run spinup doctor with a configured runner"}
	}
	version, err := merge.CheckGitVersion(ctx, runner)
	if err != nil {
		return failure(err, doctor.Fail, "install git 2.44 or later")
	}
	return doctor.Result{Status: doctor.OK, Message: version}
}
func lockValid(_ context.Context, c *doctor.Context) doctor.Result {
	if skipped(c) {
		return skip()
	}
	if _, err := manifest.Load(filepath.Join(c.Root, project.LockFile)); err != nil {
		return failure(err, doctor.Fail, "repair spinup.lock")
	}
	return doctor.Result{Status: doctor.OK, Message: "spinup.lock is valid"}
}
func managedMissing(_ context.Context, c *doctor.Context) doctor.Result {
	if skipped(c) {
		return skip()
	}
	lock, err := manifest.Load(filepath.Join(c.Root, project.LockFile))
	if err != nil {
		return failure(err, doctor.Warn, "repair spinup.lock")
	}
	statuses, err := lock.Status(c.Root)
	if err != nil {
		return failure(err, doctor.Warn, "check the managed file permissions")
	}
	paths := []string{}
	for _, s := range statuses {
		if s.Entry.Class == manifest.Managed && s.State == manifest.Missing {
			paths = append(paths, s.Path)
		}
	}
	if len(paths) > 0 {
		return doctor.Result{Status: doctor.Warn, Message: "managed files missing: " + strings.Join(paths, ", "), Try: "spinup restore " + paths[0]}
	}
	return doctor.Result{Status: doctor.OK, Message: "managed files are present"}
}
func kitVersion(ctx context.Context, c *doctor.Context) doctor.Result {
	if skipped(c) {
		return skip()
	}
	version, _ := c.Deps["cli_version"].(string)
	if version == "dev" || version == "0.0.0-dev" {
		return doctor.Result{Status: doctor.Skip, Message: "development CLI"}
	}
	runner, ok := c.Deps["runner"].(exec.Runner)
	if !ok {
		return doctor.Result{Status: doctor.Warn, Message: "kit check requires an exec.Runner", Try: "run spinup doctor with a configured runner"}
	}
	result, err := runner.Run(ctx, exec.Cmd{Name: "go", Args: []string{"mod", "edit", "-json"}, Dir: filepath.Join(c.Root, "api")})
	if err != nil {
		return failure(err, doctor.Warn, "repair api/go.mod")
	}
	type module struct{ Path, Version string }
	var mod struct {
		Require []module
		Replace []struct{ Old, New module }
	}
	if err := json.Unmarshal(result.Stdout, &mod); err != nil {
		return failure(err, doctor.Warn, "repair api/go.mod")
	}
	const kit = "github.com/stopyellingatme/spinup/kit"
	for _, r := range mod.Replace {
		if r.Old.Path == kit && r.New.Version == "" {
			return doctor.Result{Status: doctor.Skip, Message: "kit uses a local replace"}
		}
	}
	for _, r := range mod.Require {
		if r.Path == kit {
			if err := compat.CheckKit(version, r.Version); err != nil {
				return failure(err, doctor.Warn, "spinup upgrade")
			}
			return doctor.Result{Status: doctor.OK, Message: fmt.Sprintf("kit %s matches CLI %s", r.Version, version)}
		}
	}
	return doctor.Result{Status: doctor.Warn, Message: "api/go.mod does not require the kit", Try: "spinup upgrade"}
}
