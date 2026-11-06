package flagsdrift

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

func init() {
	doctor.Register(doctor.Check{ID: "flags-drift", Title: "Flag configuration drift", Scope: doctor.Local, Quick: true, Run: runCheck})
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
	if !slices.Contains(lock.Packs, "flags") {
		return skip("no flags module")
	}
	config, err := project.Load(c.Root)
	if err != nil {
		return warn("spinup.toml has invalid flags.", "correct [flags] in spinup.toml and run spinup g flags")
	}
	decls := config.FlagDecls()
	if decls.Validate() != nil || config.AuthRoles().Validate() != nil {
		return warn("spinup.toml has invalid flags.", "correct [flags] in spinup.toml and run spinup g flags")
	}
	for _, file := range []struct {
		path string
		data []byte
	}{{project.FlagsFilePath, project.RenderFlagsFile(decls)}, {project.FlagsTSFilePath, project.RenderFlagsTSFile(decls, config.AuthRoles())}} {
		got, err := os.ReadFile(filepath.Join(c.Root, filepath.FromSlash(file.path)))
		if err != nil || !bytes.Equal(got, file.data) {
			return warn(file.path+" does not match [flags] in spinup.toml.", "spinup g flags")
		}
	}
	now, ok := c.Deps["now"].(time.Time)
	if !ok {
		now = time.Now().UTC()
	}
	today := now.UTC().Format(time.DateOnly)
	var expired []string
	for _, f := range decls {
		if f.Expires != "" && f.Expires < today {
			expired = append(expired, fmt.Sprintf("%s (%s)", f.Key, f.Expires))
		}
	}
	if len(expired) > 0 {
		return warn(fmt.Sprintf("%d flags are past their expires date: %s.", len(expired), strings.Join(expired, ", ")), "remove each [flags.<key>] table and its uses from the code, run spinup g flags, then flags prune --yes")
	}
	return doctor.Result{Status: doctor.OK, Message: "generated flags match [flags] in spinup.toml"}
}
func skip(message string) doctor.Result { return doctor.Result{Status: doctor.Skip, Message: message} }
func warn(message, hint string) doctor.Result {
	return doctor.Result{Status: doctor.Warn, Message: message, Try: hint}
}
