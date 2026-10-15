// Package drift compares an application with its locked templates and applies ownership changes.
package drift

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	textdiff "github.com/rogpeppe/go-internal/diff"
	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/templates/store"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/upgrade"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Options supplies command inputs and an optional template source for tests.
type Options struct {
	Dir, Version          string
	Offline, JSON, DryRun bool
	Source                templates.Source
}

type application struct {
	root   string
	lock   *manifest.Lock
	plan   vfs.Plan
	source templates.Source
}

func find(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return "", fmt.Errorf("cannot locate application: %w\nTry: run inside a spinup project or pass --dir", err)
	}
	return root, nil
}

func load(ctx context.Context, o Options) (*application, error) {
	root, err := find(o.Dir)
	if err != nil {
		return nil, err
	}
	lock, err := manifest.Load(filepath.Join(root, project.LockFile))
	if err != nil {
		return nil, err
	}
	return renderApp(ctx, o, root, lock)
}

func renderApp(ctx context.Context, o Options, root string, lock *manifest.Lock) (*application, error) {
	source := o.Source
	if source == nil {
		var err error
		source, err = store.Default(o.Version, o.Offline)
		if err != nil {
			return nil, err
		}
	}
	version, err := source.Load(ctx, lock.TemplateVersion)
	if err != nil {
		return nil, err
	}
	plan, err := upgrade.Render(version, lock.Packs, lock.Answers)
	if err != nil {
		return nil, err
	}
	return &application{root: root, lock: lock, plan: plan, source: source}, nil
}

// File describes a path that differs from its template.
type File struct {
	Path  string         `json:"path"`
	Class manifest.Class `json:"class"`
	State string         `json:"state"`
}

// Diff prints template-to-disk differences in path order.
func Diff(ctx context.Context, o Options, paths []string, out *ui.Printer) error {
	app, err := load(ctx, o)
	if err != nil {
		return err
	}
	for i, name := range paths {
		clean, err := vfs.CleanPath(name)
		if err != nil {
			return err
		}
		paths[i] = clean
	}
	names := map[string]bool{}
	for name := range app.lock.Files {
		names[name] = true
	}
	for _, name := range app.lock.Tombstones {
		names[name] = true
	}
	for _, file := range app.plan.Files {
		names[file.Path] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	root, err := os.OpenRoot(app.root)
	if err != nil {
		return diskError(err)
	}
	defer root.Close()
	files := []File{}
	for _, name := range ordered {
		if !selected(name, paths) {
			continue
		}
		entry, tracked := app.lock.Files[name]
		rendered, exists := app.plan.Get(name)
		file := File{Path: name, Class: entry.Class}
		var data []byte
		switch {
		case slices.Contains(app.lock.Tombstones, name):
			file.State = "tombstone"
			file.Class = manifest.Class(rendered.Class)
		case !tracked:
			file.State = "untracked"
			file.Class = manifest.Class(rendered.Class)
		default:
			data, err = read(root, name)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				file.State = "missing"
			case err != nil:
				return diskError(err)
			case exists && !bytes.Equal(rendered.Data, data):
				file.State = "modified"
			default:
				continue
			}
		}
		files = append(files, file)
		if !o.JSON {
			if file.State == "modified" {
				if manifest.IsBinary(rendered.Data) || manifest.IsBinary(data) {
					out.Printf("%s: binary files differ\n", name)
				} else {
					out.Printf("%s", textdiff.Diff("a/"+name, rendered.Data, "b/"+name, data))
				}
			} else {
				out.Printf("%s\t%s\t%s\n", file.State, file.Class, name)
			}
		}
	}
	if o.JSON {
		return out.JSON(struct {
			Files []File `json:"files"`
		}{files})
	}
	return nil
}

func selected(name string, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, p := range paths {
		if name == p || strings.HasPrefix(name, p+"/") {
			return true
		}
	}
	return false
}

// Problem identifies a CI failure and its suggested correction.
type Problem struct {
	Path    string `json:"path"`
	State   string `json:"state"`
	Message string `json:"message"`
	Try     string `json:"try"`
}

// Check reports lock problems, managed drift, conflict markers, and newer templates.
func Check(ctx context.Context, o Options, allowBehind bool, out *ui.Printer) error {
	root, err := find(o.Dir)
	if err != nil {
		return err
	}
	lock, err := manifest.Load(filepath.Join(root, project.LockFile))
	problems := []Problem{}
	if err != nil {
		problems = append(problems, Problem{Path: project.LockFile, State: "invalid-lock", Message: err.Error(), Try: "repair spinup.lock"})
	} else {
		app, err := renderApp(ctx, o, root, lock)
		if err != nil {
			return err
		}
		problems, err = inspect(root, lock)
		if err != nil {
			return err
		}
		if !allowBehind {
			versions, err := app.source.Versions(ctx)
			if err != nil {
				return err
			}
			current, err := compat.ParseVersion(lock.TemplateVersion)
			if err != nil {
				return fmt.Errorf("cannot compare template version: %w\nTry: repair spinup.lock", err)
			}
			for i := len(versions) - 1; i >= 0; i-- {
				v, err := compat.ParseVersion(versions[i])
				if err != nil {
					return err
				}
				if v.Compare(current) > 0 {
					if _, err := app.source.Load(ctx, versions[i]); err != nil {
						return err
					}
					problems = append(problems, Problem{Path: project.LockFile, State: "behind", Message: "newer template available: " + versions[i], Try: "spinup upgrade"})
					break
				}
			}
		}
	}
	if o.JSON {
		if err := out.JSON(struct {
			Problems []Problem `json:"problems"`
		}{problems}); err != nil {
			return err
		}
	} else {
		for _, p := range problems {
			out.Printf("%s: %s (%s)\nTry: %s\n", p.Path, p.Message, p.State, p.Try)
		}
	}
	if len(problems) > 0 {
		return &ui.Error{What: "Application check failed", Why: "the project has drift", Try: "correct the reported problems and run spinup check", Code: ui.ExitDrift}
	}
	return nil
}

func inspect(dir string, lock *manifest.Lock) ([]Problem, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, diskError(err)
	}
	defer root.Close()
	paths := make([]string, 0, len(lock.Files))
	for name := range lock.Files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	problems := []Problem{}
	for _, name := range paths {
		entry := lock.Files[name]
		data, err := read(root, name)
		if errors.Is(err, fs.ErrNotExist) {
			if entry.Class == manifest.Managed {
				problems = append(problems, Problem{name, "missing", "managed file is missing", "spinup restore " + name})
			}
			continue
		}
		if err != nil {
			return nil, diskError(err)
		}
		if entry.Class == manifest.Managed && manifest.Hash(data) != entry.Hash {
			problems = append(problems, Problem{name, "drift", "managed file differs from its lock hash", "spinup eject " + name + " or spinup restore " + name})
		}
		if conflictMarkers(data) {
			problems = append(problems, Problem{name, "conflict", "file contains conflict markers", "resolve the conflict markers in " + name})
		}
	}
	return problems, nil
}

func conflictMarkers(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("<<<<<<<")) || bytes.Equal(line, []byte("=======")) || bytes.HasPrefix(line, []byte(">>>>>>>")) {
			return true
		}
	}
	return false
}

func read(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", name)
	}
	return root.ReadFile(name)
}
func diskError(err error) error {
	return fmt.Errorf("cannot inspect application files: %w\nTry: check the file paths and permissions", err)
}

// Eject changes a managed file to scaffold ownership in the lock.
func Eject(ctx context.Context, o Options, name string) error {
	app, err := load(ctx, o)
	if err != nil {
		return err
	}
	name, err = vfs.CleanPath(name)
	if err != nil {
		return err
	}
	entry, ok := app.lock.Files[name]
	if !ok || entry.Class != manifest.Managed {
		return fmt.Errorf("cannot eject %q: the path is not managed\nTry: select a managed path with spinup diff --json", name)
	}
	entry.Class = manifest.Scaffold
	if err := app.lock.SetFile(name, entry); err != nil {
		return err
	}
	if o.DryRun {
		return nil
	}
	return app.lock.Save(filepath.Join(app.root, project.LockFile))
}

// Restore writes a rendered file and clears its tombstone.
func Restore(ctx context.Context, o Options, name string, force bool) error {
	app, err := load(ctx, o)
	if err != nil {
		return err
	}
	name, err = vfs.CleanPath(name)
	if err != nil {
		return err
	}
	file, ok := app.plan.Get(name)
	if !ok {
		return fmt.Errorf("cannot restore %q: the template does not contain this path\nTry: select a path from spinup diff", name)
	}
	if _, tracked := app.lock.Files[name]; !tracked && !slices.Contains(app.lock.Tombstones, name) {
		return fmt.Errorf("cannot restore %q: the path is not tracked or tombstoned\nTry: select a path from spinup diff", name)
	}
	root, err := os.OpenRoot(app.root)
	if err != nil {
		return diskError(err)
	}
	data, readErr := read(root, name)
	_ = root.Close()
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return diskError(readErr)
	}
	if readErr == nil && !bytes.Equal(data, file.Data) && !force {
		return fmt.Errorf("cannot restore %q: the existing file differs from the template\nTry: spinup restore %s --force", name, name)
	}
	class := manifest.Class(file.Class)
	if entry, ok := app.lock.Files[name]; ok {
		class = entry.Class
	}
	if err := app.lock.SetFile(name, manifest.FileEntry{Class: class, Template: file.Template, Hash: manifest.Hash(file.Data), Mode: fmt.Sprintf("%04o", file.Mode.Perm())}); err != nil {
		return err
	}
	if o.DryRun {
		return nil
	}
	if _, err := vfs.Apply(app.root, vfs.Plan{Files: []vfs.File{file}}, vfs.ApplyOptions{}); err != nil {
		return fmt.Errorf("cannot restore %q: %w\nTry: check the file permissions", name, err)
	}
	return app.lock.Save(filepath.Join(app.root, project.LockFile))
}
