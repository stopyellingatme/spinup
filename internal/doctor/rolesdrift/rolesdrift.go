package rolesdrift

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

const mainPath = "api/cmd/api/main.go"

func init() {
	doctor.Register(doctor.Check{ID: "roles-drift", Title: "Role configuration drift", Scope: doctor.Local, Quick: true, Run: runCheck})
}

func runCheck(_ context.Context, c *doctor.Context) doctor.Result {
	if c.Root == "" {
		return skip("outside a spinup project")
	}
	lock, err := manifest.Load(filepath.Join(c.Root, project.LockFile))
	if errors.Is(err, os.ErrNotExist) {
		return skip("outside a spinup project: spinup.lock is missing")
	}
	if err != nil {
		return warn("cannot read spinup.lock", "correct spinup.lock")
	}
	if !slices.Contains(lock.Packs, "auth") {
		return skip("no auth module")
	}
	config, err := project.Load(c.Root)
	if err != nil {
		return warn("cannot read spinup.toml", "correct spinup.toml")
	}
	roles := config.AuthRoles()
	if err := roles.Validate(); err != nil {
		return warn("spinup.toml has invalid roles", "correct [auth] in spinup.toml and run spinup g roles")
	}
	want := project.RenderRolesFile(roles)
	path := filepath.Join(c.Root, filepath.FromSlash(project.RolesFilePath))
	got, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return warn("cannot read "+project.RolesFilePath, "check the file permissions")
	}
	if errors.Is(err, os.ErrNotExist) || !bytes.Equal(got, want) {
		return warn(project.RolesFilePath+" does not match [auth] in spinup.toml.", "spinup g roles")
	}
	main, err := os.ReadFile(filepath.Join(c.Root, filepath.FromSlash(mainPath)))
	if err != nil {
		return warn("cannot read "+mainPath, "check the file permissions")
	}
	if !bytes.Contains(main, []byte("auth.WithRoles(")) || !bytes.Contains(main, []byte("app.WithCommand(\"user\", auth.UserCommand(a))")) {
		return warn(mainPath+" does not pass the role set to the auth module.", "add auth.WithRoles(roles.Set), auth.WithRegistration(...) and app.WithCommand(\"user\", auth.UserCommand(a)) to main.go")
	}
	return doctor.Result{Status: doctor.OK, Message: "generated roles match [auth] in spinup.toml"}
}

func skip(reason string) doctor.Result {
	return doctor.Result{Status: doctor.Skip, Message: reason}
}

func warn(message, try string) doctor.Result {
	return doctor.Result{Status: doctor.Warn, Message: message, Try: try}
}
