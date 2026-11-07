package billingdrift

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

func init() {
	doctor.Register(doctor.Check{ID: "billing-drift", Title: "Billing configuration drift", Scope: doctor.Local, Quick: true, Run: runCheck})
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
	if !slices.Contains(lock.Packs, "billing") {
		return skip("no billing module")
	}
	config, err := project.Load(c.Root)
	if err != nil {
		return warn("spinup.toml has invalid billing: "+strings.Split(err.Error(), "\nTry:")[0], "correct [billing] in spinup.toml and run spinup g billing")
	}
	decls := config.BillingConfig()
	if err := decls.Validate(config.BillingContext()); err != nil {
		return warn("spinup.toml has invalid billing: "+strings.Split(err.Error(), "\nTry:")[0], "correct [billing] in spinup.toml and run spinup g billing")
	}
	var drift []string
	for _, file := range []struct {
		path string
		data []byte
	}{{project.BillingFilePath, project.RenderBillingFile(decls)}, {project.BillingTSFilePath, project.RenderBillingTSFile(decls)}} {
		got, err := os.ReadFile(filepath.Join(c.Root, filepath.FromSlash(file.path)))
		if err != nil || !bytes.Equal(got, file.data) {
			drift = append(drift, file.path)
		}
	}
	if len(drift) > 0 {
		return warn(strings.Join(drift, ", ")+" does not match [billing] in spinup.toml.", "run spinup g billing")
	}
	return doctor.Result{Status: doctor.OK, Message: "the generated billing files match [billing] in spinup.toml"}
}
func skip(message string) doctor.Result { return doctor.Result{Status: doctor.Skip, Message: message} }
func warn(message, hint string) doctor.Result {
	return doctor.Result{Status: doctor.Warn, Message: message, Try: hint}
}
