package apikeysdrift

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

func init() {
	doctor.Register(doctor.Check{ID: "apikeys-drift", Title: "Flag configuration drift", Scope: doctor.Local, Quick: true, Run: runCheck})
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
	if !slices.Contains(lock.Packs, "apikeys") {
		return skip("no apikeys module")
	}
	config, err := project.Load(c.Root)
	if err != nil {
		return warn("spinup.toml has invalid apikeys.", "correct [apikeys] in spinup.toml and run spinup g apikeys")
	}
	decls := config.APIKeysConfig()
	if decls.Validate() != nil || config.AuthRoles().Validate() != nil {
		return warn("spinup.toml has invalid apikeys.", "correct [apikeys] in spinup.toml and run spinup g apikeys")
	}
	for _, file := range []struct {
		path string
		data []byte
	}{{project.APIKeysFilePath, project.RenderAPIKeysFile(decls)}, {project.APIKeysTSFilePath, project.RenderAPIKeysTSFile(decls, config.AuthRoles())}} {
		got, err := os.ReadFile(filepath.Join(c.Root, filepath.FromSlash(file.path)))
		if err != nil || !bytes.Equal(got, file.data) {
			return warn(file.path+" does not match [apikeys] in spinup.toml.", "run spinup g apikeys")
		}
	}
	return doctor.Result{Status: doctor.OK, Message: "generated apikeys match [apikeys] in spinup.toml"}
}
func skip(message string) doctor.Result { return doctor.Result{Status: doctor.Skip, Message: message} }
func warn(message, hint string) doctor.Result {
	return doctor.Result{Status: doctor.Warn, Message: message, Try: hint}
}
