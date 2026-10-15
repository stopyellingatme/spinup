package clientdrift

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen/client"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func init() {
	doctor.Register(doctor.Check{ID: "client-drift", Title: "TypeScript client drift", Scope: doctor.Local, Run: runCheck})
}

func runCheck(ctx context.Context, c *doctor.Context) doctor.Result {
	if c.Root == "" {
		return skip("outside a spinup project")
	}
	if c.Quick {
		return skip("requires a full check")
	}
	lock, err := manifest.Load(filepath.Join(c.Root, "spinup.lock"))
	if errors.Is(err, os.ErrNotExist) {
		return skip("outside a spinup project: spinup.lock is missing")
	}
	if err != nil {
		return warning("cannot read spinup.lock: %v", err)
	}
	if !slices.Contains(lock.Packs, "web") {
		return skip("no web pack")
	}
	for _, path := range []string{"api/openapi/bundle.gen.yaml", "web/node_modules"} {
		info, err := os.Stat(filepath.Join(c.Root, filepath.FromSlash(path)))
		if errors.Is(err, os.ErrNotExist) {
			return skip(path + " is missing")
		}
		if err != nil {
			return warning("cannot inspect %s: %v", path, err)
		}
		if path == "web/node_modules" && !info.IsDir() {
			return warning("web/node_modules is not a directory")
		}
	}
	runner, ok := c.Deps["runner"].(exec.Runner)
	if !ok {
		runner = exec.OSRunner{}
	}
	// Keep the temporary output outside the project and always remove it.
	dir, err := os.MkdirTemp("", "spinup-client-drift-*")
	if err != nil {
		return warning("cannot create temporary client directory: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	output := filepath.Join(dir, "schema.d.ts")
	if err := client.GenerateTypes(ctx, c.Root, output, runner); err != nil {
		return warning("cannot generate TypeScript client: %v", err)
	}
	fresh, err := os.ReadFile(output)
	if err != nil {
		return warning("cannot read generated TypeScript client: %v", err)
	}
	disk, err := os.ReadFile(filepath.Join(c.Root, "web/src/lib/api/schema.d.ts"))
	if err != nil {
		return warning("cannot read web/src/lib/api/schema.d.ts: %v", err)
	}
	if !bytes.Equal(normalize(fresh), normalize(disk)) {
		return warning("web/src/lib/api/schema.d.ts is stale")
	}
	return doctor.Result{Status: doctor.OK, Message: "TypeScript client matches the OpenAPI bundle"}
}

func normalize(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}

func skip(reason string) doctor.Result {
	return doctor.Result{Status: doctor.Skip, Message: reason}
}

func warning(format string, args ...any) doctor.Result {
	message := fmt.Sprintf(format, args...)
	message, _, _ = strings.Cut(message, "\nTry:")
	return doctor.Result{Status: doctor.Warn, Message: strings.Join(strings.Fields(message), " "), Try: "spinup g client"}
}
