// Package render integrates verified plugin data with template rendering and generators.
package render

import (
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

type overlay struct {
	core    fs.FS
	plugins []plugin.Loaded
}

func (o *overlay) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		file, err := o.core.Open(".")
		if err != nil {
			return nil, err
		}
		entries, err := o.ReadDir(".")
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		return &rootDirectory{File: file, entries: entries}, nil
	}
	first, rest, hasRest := strings.Cut(name, "/")
	for _, p := range o.plugins {
		if first == p.Meta.Name {
			if !hasRest {
				rest = "."
			}
			return p.FS.Open(rest)
		}
	}
	return o.core.Open(name)
}

func (o *overlay) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		first, rest, hasRest := strings.Cut(name, "/")
		for _, p := range o.plugins {
			if first == p.Meta.Name {
				if !hasRest {
					rest = "."
				}
				return fs.ReadDir(p.FS, rest)
			}
		}
		return fs.ReadDir(o.core, name)
	}
	entries, err := fs.ReadDir(o.core, ".")
	if err != nil {
		return nil, err
	}
	for _, p := range o.plugins {
		info, err := fs.Stat(p.FS, ".")
		if err != nil {
			return nil, err
		}
		entries = append(entries, namedEntry{DirEntry: fs.FileInfoToDirEntry(info), name: p.Meta.Name})
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

type namedEntry struct {
	fs.DirEntry
	name string
}

func (e namedEntry) Name() string { return e.name }

// Overlay adds verified plugin packs without changing the input version or names.
// Each dependency must already be selected. Duplicate pack names are rejected.
func Overlay(v *templates.Version, names []string, plugins []plugin.Loaded) (*templates.Version, []string, error) {
	if v == nil || v.Packs == nil {
		return nil, nil, fmt.Errorf("load plugin packs: template version is missing\nTry: load the template version first")
	}
	if len(plugins) == 0 {
		return v, slices.Clone(names), nil
	}
	core, err := templates.List(v.Packs)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, name := range core {
		seen[name] = true
	}
	o := &overlay{core: v.Packs, plugins: slices.Clone(plugins)}
	selected := slices.Clone(names)
	for _, p := range plugins {
		if seen[p.Meta.Name] {
			return nil, nil, fmt.Errorf("load plugin %q: pack name collides with another pack\nTry: rename the plugin pack", p.Meta.Name)
		}
		seen[p.Meta.Name] = true
		if !slices.Contains(selected, p.Meta.Name) {
			selected = append(selected, p.Meta.Name)
		}
	}
	for _, p := range plugins {
		pack, err := templates.Load(o, p.Meta.Name)
		if err != nil {
			return nil, nil, err
		}
		for _, required := range pack.Requires {
			if !slices.Contains(selected, required) {
				return nil, nil, fmt.Errorf("load plugin %q: required pack %q is not selected\nTry: enable the required module before adding the plugin", p.Meta.Name, required)
			}
		}
	}
	result := *v
	result.Packs = o
	return &result, selected, nil
}

// Prefix records the plugin namespace for files rendered through an overlay.
func Prefix(fsys fs.FS, plan vfs.Plan) vfs.Plan {
	o, ok := fsys.(*overlay)
	if !ok {
		return plan
	}
	result := vfs.Plan{Files: slices.Clone(plan.Files)}
	for i, file := range result.Files {
		for _, p := range o.plugins {
			if strings.HasPrefix(file.Template, p.Meta.Name+"/") {
				result.Files[i].Template = "plugin/" + file.Template
				break
			}
		}
	}
	return result
}

// Plugins returns the plugin identities represented by the pack filesystem.
func Plugins(fsys fs.FS) []manifest.Plugin {
	o, ok := fsys.(*overlay)
	if !ok {
		return nil
	}
	entries := make([]manifest.Plugin, 0, len(o.plugins))
	for _, p := range o.plugins {
		entries = append(entries, manifest.Plugin{Name: p.Meta.Name, Version: p.Meta.Version, API: p.Meta.API, SHA256: p.SHA256})
	}
	return entries
}

type rootDirectory struct {
	fs.File
	entries []fs.DirEntry
	offset  int
}

func (d *rootDirectory) ReadDir(n int) ([]fs.DirEntry, error) {
	if n > 0 && d.offset >= len(d.entries) {
		return nil, io.EOF
	}
	end := len(d.entries)
	if n > 0 && d.offset+n < end {
		end = d.offset + n
	}
	entries := slices.Clone(d.entries[d.offset:end])
	d.offset = end
	return entries, nil
}
