package gendrift

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen/openapi"
	"github.com/stopyellingatme/spinup/internal/gen/run"
	"go.yaml.in/yaml/v3"
)

func init() {
	doctor.Register(doctor.Check{ID: "openapi-drift", Title: "OpenAPI generation drift", Scope: doctor.Local, Run: openAPICheck})
	doctor.Register(doctor.Check{ID: "sqlc-drift", Title: "SQL generation drift", Scope: doctor.Local, Run: sqlcCheck})
}

func openAPICheck(ctx context.Context, c *doctor.Context) doctor.Result {
	const fix = "spinup g openapi"
	if c.Root == "" {
		return skip("outside a spinup project")
	}
	if c.Quick {
		return skip("requires a full check")
	}
	apiDir := filepath.Join(c.Root, "api")
	if _, err := os.Stat(filepath.Join(apiDir, "openapi")); errors.Is(err, os.ErrNotExist) {
		return skip("api/openapi is missing")
	} else if err != nil {
		return fail(fix, "cannot inspect api/openapi: %v", err)
	}
	fresh, err := openapi.Bundle(apiDir)
	if err != nil {
		return fail(fix, "%v", err)
	}
	disk, err := os.ReadFile(filepath.Join(apiDir, "openapi/bundle.gen.yaml"))
	if err != nil {
		return fail(fix, "cannot read api/openapi/bundle.gen.yaml: %v", err)
	}
	if !bytes.Equal(fresh, disk) {
		return fail(fix, "api/openapi/bundle.gen.yaml is stale")
	}
	var bundle struct {
		Paths map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(fresh, &bundle); err != nil {
		return fail(fix, "cannot read bundled paths: %v", err)
	}
	if len(bundle.Paths) == 0 {
		return doctor.Result{Status: doctor.OK, Message: "OpenAPI bundle matches its sources. No paths to generate"}
	}

	config, err := os.ReadFile(filepath.Join(apiDir, "oapi-codegen.yaml"))
	if err != nil {
		return fail(fix, "cannot read api/oapi-codegen.yaml: %v", err)
	}
	var options map[string]any
	if err := yaml.Unmarshal(config, &options); err != nil {
		return fail(fix, "cannot read api/oapi-codegen.yaml: %v", err)
	}
	output, ok := options["output"].(string)
	if !ok || output == "" {
		return fail(fix, "cannot check Go types: api/oapi-codegen.yaml has no output path")
	}
	dir, err := os.MkdirTemp("", "spinup-openapi-drift-*")
	if err != nil {
		return fail(fix, "cannot create temporary OpenAPI directory: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// In oapi-codegen v2.8.0, -o does not override output in the config.
	// Keep all other options and the working directory of normal generation.
	temporaryOutput := filepath.Join(dir, "api.gen.go")
	options["output"] = temporaryOutput
	config, err = yaml.Marshal(options)
	if err != nil {
		return fail(fix, "cannot encode temporary oapi-codegen config: %v", err)
	}
	temporaryConfig := filepath.Join(dir, "oapi-codegen.yaml")
	if err := os.WriteFile(temporaryConfig, config, 0600); err != nil {
		return fail(fix, "cannot write temporary oapi-codegen config: %v", err)
	}
	runner := checkRunner(c)
	cmd := run.ToolCommand(ctx, apiDir, runner, "oapi-codegen", "-config", temporaryConfig, "openapi/bundle.gen.yaml")
	result, err := runner.Run(ctx, cmd)
	if err != nil || result.ExitCode != 0 {
		return toolFailure("oapi-codegen", fix, result, err)
	}
	generated, err := os.ReadFile(temporaryOutput)
	if err != nil {
		return fail(fix, "cannot read temporary Go types: %v", err)
	}
	committed := output
	if !filepath.IsAbs(committed) {
		committed = filepath.Join(apiDir, committed)
	}
	disk, err = os.ReadFile(committed)
	if err != nil {
		return fail(fix, "cannot read oapi-codegen output %s: %v", output, err)
	}
	if !bytes.Equal(generated, disk) {
		return fail(fix, "oapi-codegen output %s is stale", output)
	}
	return doctor.Result{Status: doctor.OK, Message: "OpenAPI bundle and Go types match their sources"}
}

func sqlcCheck(ctx context.Context, c *doctor.Context) doctor.Result {
	const fix = "spinup db sqlc"
	if c.Root == "" {
		return skip("outside a spinup project")
	}
	if c.Quick {
		return skip("requires a full check")
	}
	apiDir := filepath.Join(c.Root, "api")
	if _, err := os.Stat(filepath.Join(apiDir, "sqlc.yaml")); errors.Is(err, os.ErrNotExist) {
		return skip("api/sqlc.yaml is missing")
	} else if err != nil {
		return fail(fix, "cannot inspect api/sqlc.yaml: %v", err)
	}
	hasQueries := false
	err := filepath.WalkDir(filepath.Join(apiDir, "db/queries"), func(_ string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			hasQueries = true
		}
		return nil
	})
	if err != nil {
		return fail(fix, "cannot inspect api/db/queries: %v", err)
	}
	if !hasQueries {
		return skip("api/db/queries has no .sql files")
	}
	// sqlc diff generates in memory and reads the existing output files.
	// The app config uses local schemas, so no database is required.
	runner := checkRunner(c)
	cmd := run.ToolCommand(ctx, apiDir, runner, "sqlc", "diff")
	result, err := runner.Run(ctx, cmd)
	if err != nil || result.ExitCode != 0 {
		return toolFailure("sqlc diff", fix, result, err)
	}
	return doctor.Result{Status: doctor.OK, Message: "sqlc output matches its sources"}
}

func checkRunner(c *doctor.Context) exec.Runner {
	if runner, ok := c.Deps["runner"].(exec.Runner); ok {
		return runner
	}
	return exec.OSRunner{}
}

func toolFailure(tool, fix string, result exec.Result, err error) doctor.Result {
	message := strings.TrimSpace(string(result.Stdout) + string(result.Stderr))
	if message == "" {
		if err != nil {
			message = err.Error()
		} else {
			message = fmt.Sprintf("exit code %d", result.ExitCode)
		}
	}
	// A tool cannot establish drift when Go cannot start or resolve its modules.
	var notFound *exec.NotFoundError
	unavailable := errors.As(err, &notFound) || (err != nil && result.ExitCode == 0)
	for _, reason := range []string{"module lookup disabled", "GOPROXY=off", "toolchain not available", "cannot find GOROOT", "missing go.sum entry", "updates to go.mod needed", "updates to go.sum needed", "no such tool"} {
		unavailable = unavailable || strings.Contains(message, reason)
	}
	lines := strings.Split(message, "\n")
	if len(lines) > 20 {
		message = strings.Join(lines[:20], "\n")
	}
	if unavailable {
		return skip("cannot run " + tool + ": " + message)
	}
	return fail(fix, "%s failed: %s", tool, message)
}

func skip(reason string) doctor.Result { return doctor.Result{Status: doctor.Skip, Message: reason} }

func fail(fix, format string, args ...any) doctor.Result {
	message := fmt.Sprintf(format, args...)
	message, _, _ = strings.Cut(message, "\nTry:")
	return doctor.Result{Status: doctor.Fail, Message: message, Try: fix}
}
