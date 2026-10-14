package templates

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Question declares an answer and its flag. Bool defaults use "true" or "false".
// HasDefault distinguishes an explicit empty default from an absent default.
// The modules key uses []string answers and a comma-separated string default.
type Question struct {
	Key, Prompt, Type, Default, Pattern string
	Choices                             []string
	defaultSet                          bool
}

// HasDefault reports whether the question declares a default.
// For directly constructed questions, a nonempty Default declares a default.
func (q Question) HasDefault() bool { return q.defaultSet || q.Default != "" }

// FileSpec declares a source, its output path, and its upgrade policy.
type FileSpec struct {
	Path        string `toml:"path"`
	Source      string `toml:"source"`
	Class       string `toml:"class"`
	When        string `toml:"when"`
	Since       string `toml:"since"`
	RenamedFrom string `toml:"renamed_from"`
	Mode        string `toml:"mode"`
	condition   *condition
}

// Pack contains merged metadata and a file system rooted at its directory.
type Pack struct {
	Name, Description string
	Requires          []string
	Questions         []Question
	Files             []FileSpec
	FS                fs.FS
}

type rawQuestion struct {
	Key     string   `toml:"key"`
	Prompt  string   `toml:"prompt"`
	Type    string   `toml:"type"`
	Default any      `toml:"default"`
	Pattern string   `toml:"pattern"`
	Choices []string `toml:"choices"`
}
type metadata struct {
	Name        *string       `toml:"name"`
	Description *string       `toml:"description"`
	Requires    []string      `toml:"requires"`
	Questions   []rawQuestion `toml:"questions"`
	Files       []FileSpec    `toml:"files"`
}

// Load reads name/pack.toml and appends pack.d fragments in filename order.
// name is the directory name, including the underscore for internal packs.
func Load(fsys fs.FS, name string) (*Pack, error) {
	if fsys == nil || !validName(name) {
		return nil, fmt.Errorf("load pack %q: invalid file system or name\nTry: use a pack directory name", name)
	}
	packFS, err := fs.Sub(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("load pack %q: %w\nTry: select an existing pack", name, err)
	}
	p := &Pack{FS: packFS}
	fragments, err := fs.Glob(packFS, "pack.d/*.toml")
	if err != nil {
		return nil, fmt.Errorf("list fragments for %q: %w\nTry: check the pack file system", name, err)
	}
	sort.Strings(fragments)
	sources := append([]string{"pack.toml"}, fragments...)
	questionSources, fileSources := map[string]string{}, map[string]string{}
	for i, source := range sources {
		data, err := fs.ReadFile(packFS, source)
		if err != nil {
			return nil, fmt.Errorf("read pack %s/%s: %w\nTry: provide a readable pack file", name, source, err)
		}
		var meta metadata
		decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
		if err := decoder.Decode(&meta); err != nil {
			return nil, fmt.Errorf("decode pack %s/%s: %w\nTry: correct the pack TOML", name, source, err)
		}
		if i == 0 {
			if meta.Name == nil || *meta.Name != strings.TrimPrefix(name, "_") {
				return nil, fmt.Errorf("load pack %q: name must equal %q\nTry: correct name in pack.toml", name, strings.TrimPrefix(name, "_"))
			}
			p.Name = *meta.Name
			if meta.Description != nil {
				p.Description = *meta.Description
			}
			p.Requires = meta.Requires
		} else if meta.Name != nil || meta.Description != nil || meta.Requires != nil {
			return nil, fmt.Errorf("load fragment %s/%s: fragments may only add questions and files\nTry: keep pack metadata in pack.toml", name, source)
		}
		origin := path.Join(name, source)
		for _, raw := range meta.Questions {
			if first, ok := questionSources[raw.Key]; ok {
				return nil, duplicate("question", raw.Key, first, origin)
			}
			q := Question{Key: raw.Key, Prompt: raw.Prompt, Type: raw.Type, Pattern: raw.Pattern, Choices: raw.Choices, defaultSet: raw.Default != nil}
			switch value := raw.Default.(type) {
			case nil:
			case string:
				q.Default = value
			case bool:
				q.Default = strconv.FormatBool(value)
			default:
				return nil, fmt.Errorf("load question %q in %s: default must be a string or bool\nTry: correct the default type", raw.Key, origin)
			}
			if raw.Default != nil {
				_, isBool := raw.Default.(bool)
				if isBool && raw.Type != "bool" {
					return nil, fmt.Errorf("load question %q in %s: bool default needs type bool\nTry: correct the question type", raw.Key, origin)
				}
			}
			questionSources[q.Key] = origin
			p.Questions = append(p.Questions, q)
		}
		for _, file := range meta.Files {
			if first, ok := fileSources[file.Path]; ok {
				return nil, duplicate("output path", file.Path, first, origin)
			}
			fileSources[file.Path] = origin
			p.Files = append(p.Files, file)
		}
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("load pack %q: %w", name, err)
	}
	return p, nil
}

func duplicate(kind, key, first, second string) error {
	return fmt.Errorf("duplicate %s %q in %s and %s\nTry: give each %s a unique name", kind, key, first, second, kind)
}

// List returns sorted directory names that contain pack.toml, including internal packs.
func List(fsys fs.FS) ([]string, error) {
	if fsys == nil {
		return nil, fmt.Errorf("list packs: file system is nil\nTry: provide the embedded pack file system")
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("list packs: %w\nTry: provide a readable pack file system", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := fs.Stat(fsys, path.Join(entry.Name(), "pack.toml"))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("inspect pack %q: %w\nTry: provide readable pack metadata", entry.Name(), err)
			}
			continue
		}
		if !info.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func validName(name string) bool {
	return name != "" && name != "_" && name != "." && fs.ValidPath(name) && !strings.ContainsAny(name, "/\\")
}

// Validate checks metadata, source files, paths, question definitions, and conditions.
// It compiles When expressions for later evaluation by FileSpec.Matches.
func (p *Pack) Validate() error {
	if p == nil || p.FS == nil {
		return fmt.Errorf("validate pack: file system is nil\nTry: load a pack with its source files")
	}
	questionKeys, filePaths := map[string]bool{}, map[string]bool{}
	for _, q := range p.Questions {
		if questionKeys[q.Key] {
			return fmt.Errorf("duplicate question %q in pack %q\nTry: give each question a unique key", q.Key, p.Name)
		}
		questionKeys[q.Key] = true
		if err := q.Validate(); err != nil {
			return err
		}
	}
	for i, f := range p.Files {
		if filePaths[f.Path] {
			return fmt.Errorf("duplicate output path %q in pack %q\nTry: give each file a unique output path", f.Path, p.Name)
		}
		filePaths[f.Path] = true
		if !manifest.Class(f.Class).Valid() {
			return fmt.Errorf("validate file %q: invalid class %q\nTry: use managed, scaffold, or seed", f.Path, f.Class)
		}
		if err := cleanPath(f.Path); err != nil {
			return err
		}
		if f.RenamedFrom != "" {
			if err := cleanPath(f.RenamedFrom); err != nil {
				return err
			}
		}
		if err := cleanPath(f.Source); err != nil {
			return err
		}
		info, err := fs.Stat(p.FS, f.Source)
		if err != nil {
			return fmt.Errorf("validate source %q: %w\nTry: include the source in the pack", f.Source, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("validate source %q: source is not a regular file\nTry: select a source file", f.Source)
		}
		if _, err := f.Permissions(); err != nil {
			return err
		}
		c, err := parseCondition(f.When)
		if err != nil {
			return err
		}
		p.Files[i].condition = c
	}
	return nil
}

func cleanPath(name string) error {
	clean, err := vfs.CleanPath(name)
	if err != nil {
		return fmt.Errorf("validate path %q: %w\nTry: use a clean relative file path", name, err)
	}
	if clean != name || strings.ContainsRune(name, 0) {
		return fmt.Errorf("validate path %q: path is not clean\nTry: use a clean relative file path", name)
	}
	return nil
}

// Validate checks the key, type, choices, bool default, and pattern syntax.
func (q Question) Validate() error {
	if !keyPattern.MatchString(q.Key) || q.Key == "Stack" || q.Key == "TemplateVersion" {
		return fmt.Errorf("validate question %q: invalid or reserved key\nTry: use an answer key other than Stack or TemplateVersion", q.Key)
	}
	switch q.Type {
	case "string":
	case "bool":
		if q.HasDefault() && q.Default != "true" && q.Default != "false" {
			return fmt.Errorf("validate question %q: invalid bool default\nTry: use true or false", q.Key)
		}
	case "enum":
		if len(q.Choices) == 0 {
			return fmt.Errorf("validate question %q: enum has no choices\nTry: declare enum choices", q.Key)
		}
	default:
		return fmt.Errorf("validate question %q: invalid type %q\nTry: use string, bool, or enum", q.Key, q.Type)
	}
	if len(q.Choices) > 0 && q.Type != "enum" {
		return fmt.Errorf("validate question %q: choices require enum type\nTry: use enum or remove choices", q.Key)
	}
	if q.Pattern != "" {
		if q.Type != "string" {
			return fmt.Errorf("validate question %q: pattern requires string type\nTry: remove the pattern or use string", q.Key)
		}
		if _, err := regexp.Compile(q.Pattern); err != nil {
			return fmt.Errorf("validate question %q: invalid pattern: %w\nTry: correct the regular expression", q.Key, err)
		}
	}
	return nil
}

// Permissions returns Mode as octal permission bits, with 0644 as the default.
func (f FileSpec) Permissions() (fs.FileMode, error) {
	if f.Mode == "" {
		return 0o644, nil
	}
	if len(f.Mode) != 4 || f.Mode[0] != '0' {
		return 0, fmt.Errorf("validate mode %q for %q: expected four octal digits\nTry: use a mode such as 0644 or 0755", f.Mode, f.Path)
	}
	mode, err := strconv.ParseUint(f.Mode, 8, 9)
	if err != nil {
		return 0, fmt.Errorf("validate mode %q for %q: %w\nTry: use a mode such as 0644 or 0755", f.Mode, f.Path, err)
	}
	return fs.FileMode(mode), nil
}
