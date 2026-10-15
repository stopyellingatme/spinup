package pack

import (
	"bytes"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"text/template"

	"github.com/pelletier/go-toml/v2"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
var argPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

func validate(t tree, cliVersion string) (plugin.Meta, error) {
	var meta plugin.Meta
	data, err := fs.ReadFile(t.fs(), "plugin.toml")
	if err != nil {
		return meta, problem("read plugin metadata", err)
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&meta); err != nil {
		return meta, problem("decode plugin metadata", err)
	}
	if !namePattern.MatchString(meta.Name) {
		return meta, problem("validate plugin name", fmt.Errorf("invalid name %q", meta.Name))
	}
	if !safeVersion(meta.Version) {
		return meta, problem("validate plugin version", fmt.Errorf("invalid version %q", meta.Version))
	}
	if meta.API != plugin.API {
		return meta, problem("validate plugin API", fmt.Errorf("API %d is unsupported, expected %d", meta.API, plugin.API))
	}
	ok, err := matchesRange(meta.Spinup, cliVersion)
	if err != nil {
		return meta, problem("validate spinup range", err)
	}
	if !ok {
		return meta, problem("validate spinup range", fmt.Errorf("CLI %s does not satisfy %q", cliVersion, meta.Spinup))
	}
	p, err := loadPack(t.fs(), meta.Name)
	if err != nil {
		return meta, err
	}
	for _, question := range p.Questions {
		if !question.HasDefault() {
			return meta, fmt.Errorf("validate plugin question %q: a default is required for upgrades\nTry: add a default to the plugin question", question.Key)
		}
	}
	for _, file := range p.Files {
		if strings.Split(file.Source, "/")[0] == "@gen" {
			return meta, problem("validate plugin source", fmt.Errorf("source %q uses the reserved @gen namespace", file.Source))
		}
		if err := CheckOutputPath(file.Path); err != nil {
			return meta, err
		}
		if file.RenamedFrom != "" {
			if err := CheckOutputPath(file.RenamedFrom); err != nil {
				return meta, err
			}
		}
	}
	names := map[string]bool{}
	for _, g := range meta.Generators {
		if !namePattern.MatchString(g.Name) || names[g.Name] {
			return meta, problem("validate generator", fmt.Errorf("invalid or duplicate name %q", g.Name))
		}
		names[g.Name] = true
		args := map[string]string{}
		for _, arg := range g.Args {
			if !argPattern.MatchString(arg) || args[arg] != "" {
				return meta, problem("validate generator arguments", fmt.Errorf("invalid or duplicate argument %q", arg))
			}
			args[arg] = "sample"
		}
		for _, file := range g.Files {
			if !manifest.Class(file.Class).Valid() {
				return meta, problem("validate generator class", fmt.Errorf("invalid class %q", file.Class))
			}
			source, err := checkTemplatePath(file.Source, args)
			if err != nil {
				return meta, err
			}
			output, err := checkTemplatePath(file.Path, args)
			if err != nil {
				return meta, err
			}
			if err := CheckOutputPath(output); err != nil {
				return meta, err
			}
			// Argument-dependent sources can only select a file during generation.
			// Static sources must already exist. Both forms require safe paths.
			if !strings.Contains(file.Source, "{{") {
				if info, err := fs.Stat(t.fs(), source); err != nil || !info.Mode().IsRegular() {
					return meta, problem("validate generator source", fmt.Errorf("source %q is missing or not a file", source))
				}
			}
		}
	}
	return meta, nil
}

// CheckOutputPath validates a portable plugin output path or renamed_from path.
// It rejects .git segments anywhere, root .spinup and .github directories, and
// root spinup.toml and spinup.lock files. Comparisons are case-insensitive.
// Generator callers must check the final rendered path with this function,
// because metadata validation uses sample arguments and cannot check user values.
func CheckOutputPath(path string) error {
	clean, err := vfs.CleanPath(path)
	if err != nil || clean != path {
		return problem("validate plugin output", fmt.Errorf("unsafe path %q", path))
	}
	parts := strings.Split(strings.ToLower(path), "/")
	if parts[0] == ".spinup" || parts[0] == ".github" || (len(parts) == 1 && (parts[0] == "spinup.toml" || parts[0] == "spinup.lock")) {
		return problem("validate plugin output", fmt.Errorf("protected path %q", path))
	}
	for _, part := range parts {
		if part == ".git" {
			return problem("validate plugin output", fmt.Errorf("protected path %q", path))
		}
	}
	return nil
}

func checkTemplatePath(value string, args map[string]string) (string, error) {
	tmpl, err := template.New("path").Option("missingkey=error").Parse(value)
	if err != nil {
		return "", problem("parse generator path", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, args); err != nil {
		return "", problem("evaluate generator path", err)
	}
	clean, err := vfs.CleanPath(out.String())
	if err != nil || clean != out.String() {
		return "", problem("validate generator path", fmt.Errorf("unsafe path %q", out.String()))
	}
	return clean, nil
}

func loadPack(fsys fs.FS, name string) (*templates.Pack, error) {
	return templates.Load(prefixedFS{FS: fsys, prefix: name}, name)
}

type prefixedFS struct {
	fs.FS
	prefix string
}

func (p prefixedFS) Open(name string) (fs.File, error) {
	if name == p.prefix {
		return p.FS.Open(".")
	}
	if relative, ok := strings.CutPrefix(name, p.prefix+"/"); ok {
		return p.FS.Open(relative)
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func safeVersion(s string) bool {
	return token(s) && len(s) <= 128 && s != "." && s != ".." && !strings.ContainsAny(s, "/\\:") && !strings.HasPrefix(s, "-") && versionPattern.MatchString(s)
}
