// Package newapp composes template packs and applies a new application plan.
package newapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/templates/store"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

const kitModule = "github.com/stopyellingatme/spinup/kit"

// Options contains explicit answers and execution settings for a new app.
type Options struct {
	Name, Dir, Version, KitReplace            string
	APIOnly, NoGit, DryRun, JSON, Interactive bool
	Answers                                   map[string]any
}

// Result contains the rendered plan, its validated lock, and output metadata.
type Result struct {
	Dir        string
	Packs      []string
	Plan       vfs.Plan
	Lock       *manifest.Lock
	Warnings   []string
	KitReplace string
}

// Catalog loads every embedded public pack in name order.
func Catalog() ([]*templates.Pack, error) {
	fsys := templates.Embedded()
	names, err := templates.List(fsys)
	if err != nil {
		return nil, err
	}
	var packs []*templates.Pack
	for _, name := range names {
		if strings.HasPrefix(name, "_") {
			continue
		}
		p, err := templates.Load(fsys, name)
		if err != nil {
			return nil, err
		}
		packs = append(packs, p)
	}
	return packs, nil
}

// Questions returns unique questions in pack order and rejects conflicting definitions.
func Questions(packs []*templates.Pack) ([]templates.Question, error) {
	seen := map[string]templates.Question{}
	var result []templates.Question
	for _, p := range packs {
		for _, q := range p.Questions {
			if previous, ok := seen[q.Key]; ok {
				if !reflect.DeepEqual(previous, q) {
					return nil, fmt.Errorf("conflicting question %q\nTry: use packs with matching question definitions", q.Key)
				}
				continue
			}
			seen[q.Key] = q
			result = append(result, q)
		}
	}
	return result, nil
}

// Generate renders the selected packs without writing files or running commands.
func Generate(ctx context.Context, o Options, prompter Prompter) (*Result, error) {
	packs, err := Catalog()
	if err != nil {
		return nil, err
	}
	asked := packs
	if o.APIOnly {
		// An API-only app never selects the web pack, so it never asks or records its questions.
		asked = slices.DeleteFunc(slices.Clone(packs), func(p *templates.Pack) bool { return p.Name == "web" })
	}
	questions, err := Questions(asked)
	if err != nil {
		return nil, err
	}
	answers, warnings, err := completeAnswers(ctx, o, questions, prompter)
	if err != nil {
		return nil, err
	}
	selected, err := selectPacks(packs, answers, o.APIOnly)
	if err != nil {
		return nil, err
	}
	answers, err = render.ValidateAnswers(selected, answers)
	if err != nil {
		return nil, err
	}
	// Validate the positional name before it becomes a filesystem path.
	name, ok := answers["app_name"].(string)
	if !ok || name != o.Name {
		return nil, fmt.Errorf("invalid app_name\nTry: supply a valid application name")
	}
	clean, err := vfs.CleanPath(name)
	if err != nil || clean != name || strings.Contains(name, "/") {
		return nil, fmt.Errorf("invalid app_name %q\nTry: supply a portable application name", name)
	}
	dir, err := filepath.Abs(filepath.Join(o.Dir, name))
	if err != nil {
		return nil, creationError(err)
	}
	if err := checkTarget(dir); err != nil {
		return nil, err
	}
	stack := stackversions.MustLoad()
	version := store.CurrentVersion(o.Version)
	plan, err := render.Render(render.Input{Packs: selected, Answers: answers, Stack: stack, TemplateVersion: version})
	if err != nil {
		return nil, err
	}
	replace := ""
	if o.KitReplace != "" {
		replace, err = filepath.Abs(o.KitReplace)
		if err != nil {
			return nil, creationError(err)
		}
		info, err := os.Stat(replace)
		if err != nil {
			return nil, creationError(err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("kit replace %q is not a directory\nTry: set SPINUP_KIT_REPLACE to the kit directory", replace)
		}
		for i := range plan.Files {
			if plan.Files[i].Path == "api/go.mod" {
				plan.Files[i].Data = manifest.Normalize(append(plan.Files[i].Data, []byte("\nreplace "+kitModule+" => "+quoteModulePath(replace)+"\n")...))
			}
		}
	}
	lock := manifest.New()
	lock.CLIVersion = o.Version
	if lock.CLIVersion == "" {
		lock.CLIVersion = "dev"
	}
	lock.TemplateVersion = version
	lock.MinCLI = version
	lock.Kit = manifest.KitRef{Module: kitModule, Version: "v0.1.0"}
	lock.BehaviorVersion = 1
	lock.FormatterGo = runtime.Version()
	lock.Answers = answers
	// Lock all images so the version snapshot is independent of pack conditions.
	keys := make([]string, 0, len(stack.Images))
	for key := range stack.Images {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lock.StackVersions[key] = stack.Images[key].Ref()
	}
	for _, file := range plan.Files {
		lock.Files[file.Path] = manifest.FileEntry{Class: manifest.Class(file.Class), Template: file.Template, Hash: manifest.Hash(file.Data), Mode: fmt.Sprintf("%04o", file.Mode.Perm())}
	}
	names := make([]string, 0, len(selected))
	for _, p := range selected {
		names = append(names, p.Name)
	}
	lock.Packs = names
	if err := lock.Validate(); err != nil {
		return nil, err
	}
	return &Result{Dir: dir, Packs: names, Plan: plan, Lock: lock, Warnings: warnings, KitReplace: replace}, nil
}

func selectPacks(packs []*templates.Pack, answers map[string]any, apiOnly bool) ([]*templates.Pack, error) {
	available := map[string]*templates.Pack{}
	var modules []string
	for _, p := range packs {
		available[p.Name] = p
		if p.Name != "base" && p.Name != "api" && p.Name != "web" {
			modules = append(modules, p.Name)
		}
	}
	sort.Strings(modules)
	selected := map[string]*templates.Pack{}
	for _, name := range []string{"base", "api"} {
		p, ok := available[name]
		if !ok {
			return nil, fmt.Errorf("required pack %q is missing\nTry: reinstall spinup", name)
		}
		selected[name] = p
	}
	if !apiOnly {
		p, ok := available["web"]
		if !ok {
			return nil, fmt.Errorf("cannot create the web frontend: the web pack is not available\nTry: pass --api-only (the web frontend arrives in a later spinup version)")
		}
		selected["web"] = p
	}
	values, ok := answers["modules"].([]string)
	if !ok {
		return nil, fmt.Errorf("invalid modules answer\nTry: pass --modules %s", strings.Join(modules, ","))
	}
	for _, name := range values {
		p, ok := available[name]
		if !ok || name == "base" || name == "api" || name == "web" {
			return nil, fmt.Errorf("unknown module %q, available modules: %s\nTry: pass --modules %s", name, strings.Join(modules, ", "), strings.Join(modules, ","))
		}
		selected[name] = p
	}
	return orderPacks(selected)
}

func orderPacks(selected map[string]*templates.Pack) ([]*templates.Pack, error) {
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	done := map[string]bool{}
	var result []*templates.Pack
	for len(result) < len(names) {
		progressed := false
		for _, name := range names {
			if done[name] {
				continue
			}
			ready := true
			for _, required := range selected[name].Requires {
				if _, ok := selected[required]; !ok {
					return nil, fmt.Errorf("pack %q requires missing pack %q\nTry: select the required module with --modules", name, required)
				}
				if !done[required] {
					ready = false
				}
			}
			if ready {
				done[name] = true
				result = append(result, selected[name])
				progressed = true
				break
			}
		}
		if !progressed {
			return nil, fmt.Errorf("template packs contain a dependency cycle\nTry: use packs without cyclic requirements")
		}
	}
	return result, nil
}

func checkTarget(dir string) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return creationError(err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cannot create app at %q: target is not a directory\nTry: choose a new or empty directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return creationError(err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("cannot create app at %q: target directory is not empty\nTry: choose a new or empty directory", dir)
	}
	return nil
}

var semverPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

func templateVersion(version string) string {
	if !semverPattern.MatchString(version) {
		return "0.0.0-dev"
	}
	core, _, _ := strings.Cut(version, "+")
	if _, prerelease, ok := strings.Cut(core, "-"); ok {
		for _, identifier := range strings.Split(prerelease, ".") {
			if len(identifier) > 1 && identifier[0] == '0' && strings.Trim(identifier, "0123456789") == "" {
				return "0.0.0-dev"
			}
		}
	}
	return version
}
func quoteModulePath(path string) string {
	path = filepath.ToSlash(path)
	if strings.ContainsAny(path, " \t\r\n\"`()") || strings.Contains(path, "//") {
		return fmt.Sprintf("%q", path)
	}
	return path
}
func creationError(err error) error {
	return &ui.Error{What: "Cannot create the application", Why: err.Error(), Try: "check the target directory and its permissions", Err: err}
}
