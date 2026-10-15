package clientdrift

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen/client"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/webtool"
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
	tool := webtool.FromLock(lock)
	output := filepath.Join(dir, filepath.Base(tool.ClientOutput()))
	if err := client.GenerateTypes(ctx, c.Root, output, runner); err != nil {
		return warning("cannot generate TypeScript client: %v", err)
	}
	fresh, err := outputs(output, tool.HeyAPI())
	if err != nil {
		return warning("cannot read generated TypeScript client: %v", err)
	}
	disk, err := outputs(filepath.Join(c.Root, "web", tool.ClientOutput()), tool.HeyAPI())
	if err != nil {
		return warning("cannot read web/%s: %v", tool.ClientOutput(), err)
	}
	names := make([]string, 0, len(fresh)+len(disk))
	for name := range fresh {
		names = append(names, name)
	}
	for name := range disk {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range slices.Compact(names) {
		a, aok := fresh[name]
		b, bok := disk[name]
		if !aok || !bok || !bytes.Equal(normalize(a), normalize(b)) {
			return warning("web/src/lib/api/%s is stale", name)
		}
	}
	return doctor.Result{Status: doctor.OK, Message: "TypeScript client matches the OpenAPI bundle"}
}

// outputs reads every generated file, including shared resource types for heyapi.
func outputs(output string, heyapi bool) (map[string][]byte, error) {
	files := map[string][]byte{}
	if !heyapi {
		data, err := os.ReadFile(output)
		if err != nil {
			return nil, err
		}
		files[filepath.Base(output)] = data
		return files, nil
	}
	err := filepath.WalkDir(output, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("generated file %s is not regular", path)
		}
		rel, err := filepath.Rel(filepath.Dir(output), path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	schema := filepath.Join(filepath.Dir(output), "schema.d.ts")
	data, err := os.ReadFile(schema)
	if err != nil {
		return nil, err
	}
	files["schema.d.ts"] = data
	return files, nil
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
