// Package scaffold creates declarative and executable plugin projects.
package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"text/template"

	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Options selects a plugin name, kind, destination, and optional local SDK.
type Options struct {
	// Dir is the base directory for relative paths.
	Dir string
	// Name identifies the plugin and prefixes its generators.
	Name string
	// Kind selects pack or exec. An empty value selects pack.
	Kind string
	// Platform selects the executable target as GOOS-GOARCH.
	Platform string
	// Out selects the destination. An empty value selects Name.
	Out string
	// SDKPath adds a replacement for a local SDK directory.
	SDKPath string
	// DryRun validates the scaffold without writing files.
	DryRun bool
}

//go:embed templates/*
var sources embed.FS

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Create writes a deterministic plugin project into an absent or empty directory.
// DryRun validates the project without creating its destination.
func Create(o Options) (string, error) {
	if !namePattern.MatchString(o.Name) {
		return "", failure(fmt.Errorf("invalid plugin name %q", o.Name))
	}
	if _, err := vfs.CleanPath(o.Name); err != nil {
		return "", failure(err)
	}
	if o.Kind == "" {
		o.Kind = "pack"
	}
	if o.Kind != "pack" && o.Kind != "exec" {
		return "", failure(fmt.Errorf("invalid kind %q", o.Kind))
	}
	if o.Platform == "" {
		o.Platform = runtime.GOOS + "-" + runtime.GOARCH
	}
	if !regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`).MatchString(o.Platform) {
		return "", failure(fmt.Errorf("invalid platform %q", o.Platform))
	}
	if o.Out == "" {
		o.Out = o.Name
	}
	dir := o.Out
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(o.Dir, dir)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", failure(err)
	}
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", failure(fmt.Errorf("destination %q is not a regular directory", dir))
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", failure(err)
		}
		if len(entries) != 0 {
			return "", failure(fmt.Errorf("destination %q is not empty", dir))
		}
	} else if !os.IsNotExist(err) {
		return "", failure(err)
	}
	if o.SDKPath != "" {
		if !filepath.IsAbs(o.SDKPath) {
			o.SDKPath = filepath.Join(o.Dir, o.SDKPath)
		}
		o.SDKPath, err = filepath.Abs(o.SDKPath)
		if err != nil {
			return "", failure(err)
		}
	}
	files, err := render(o)
	if err != nil {
		return "", failure(err)
	}
	if o.DryRun {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return "", failure(err)
	}
	// Stage every file before publishing the directory. Existing empty directories
	// remain untouched until the complete scaffold is ready.
	stage, err := os.MkdirTemp(filepath.Dir(dir), ".spinup-plugin-*")
	if err != nil {
		return "", failure(err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := os.Chmod(stage, 0755); err != nil {
		return "", failure(err)
	}
	for _, file := range files {
		name := filepath.Join(stage, file.Path)
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return "", failure(err)
		}
		if err := os.WriteFile(name, file.Data, 0644); err != nil {
			return "", failure(err)
		}
	}
	if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
		return "", failure(err)
	}
	if err := os.Rename(stage, dir); err != nil {
		return "", failure(err)
	}
	return dir, nil
}

func render(o Options) ([]vfs.File, error) {
	names := []string{"plugin.toml", "pack.toml", "README.md", "files/note.txt.tmpl"}
	if o.Kind == "exec" {
		names = append(names, "go.mod", "main.go", "main_test.go", "Makefile", ".gitignore")
	}
	result := make([]vfs.File, 0, len(names))
	for _, name := range names {
		source := "templates/" + strings.ReplaceAll(name, "/", "_") + ".txt"
		data, err := fs.ReadFile(sources, source)
		if err != nil {
			return nil, err
		}
		tmpl, err := template.New(name).Delims("<%", "%>").Funcs(template.FuncMap{"quote": strconv.Quote}).Parse(string(data))
		if err != nil {
			return nil, err
		}
		var output bytes.Buffer
		if err := tmpl.Execute(&output, struct {
			Options
			GOOS, GOARCH string
		}{o, strings.Split(o.Platform, "-")[0], strings.Split(o.Platform, "-")[1]}); err != nil {
			return nil, err
		}
		data = output.Bytes()
		if strings.HasSuffix(name, ".go") {
			data, err = format.Source(data)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, vfs.File{Path: name, Data: data})
	}
	return result, nil
}

func failure(err error) error {
	return fmt.Errorf("create plugin scaffold: %w\nTry: use a lowercase plugin name, --kind pack or exec, and an empty destination", err)
}
