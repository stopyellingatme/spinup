package job

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/modules"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Dir is the app package that holds the jobs, relative to the project root.
const Dir = "api/internal/jobs"

// RegisterFile is the generator-owned file that registers every job.
const RegisterFile = Dir + "/register_gen.go"

// pack is the template pack that a job needs.
const pack = "jobs"

// reserved holds the names that clash with the files or identifiers of the
// jobs pack (register.go, deps.go, Register, Deps).
var reserved = map[string]bool{"register": true, "register_gen": true, "deps": true, "generated": true}

// knownGOOS and knownGOARCH match the filename selectors in Go 1.27's
// internal/syslist package. They include obsolete values because Go still
// recognizes them in file names.
var knownGOOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true,
	"js": true, "linux": true, "nacl": true, "netbsd": true,
	"openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
	"windows": true, "zos": true,
}

var knownGOARCH = map[string]bool{
	"386": true, "amd64": true, "amd64p32": true, "arm": true,
	"armbe": true, "arm64": true, "arm64be": true, "loong64": true,
	"mips": true, "mipsle": true, "mips64": true, "mips64le": true,
	"mips64p32": true, "mips64p32le": true, "ppc": true, "ppc64": true,
	"ppc64le": true, "riscv": true, "riscv64": true, "s390": true,
	"s390x": true, "sparc": true, "sparc64": true, "wasm": true,
}

func init() {
	registry.Register(gen.Generator{
		Name:  "job",
		Short: "Add a background job (River) to api/internal/jobs",
		Usage: "<name>",
		Run:   plan,
	})
}

// Name is a normalized job name.
type Name struct {
	// Snake is the job kind and the file name, such as "send_digest".
	Snake string
	// Pascal is the Go name of the worker function, such as "SendDigest".
	Pascal string
}

// ParseName normalizes a job name in snake, kebab, or camel case.
func ParseName(s string) (Name, error) {
	words, ok := splitWords(s)
	if !ok || len(words) == 0 {
		return Name{}, nameError(s, "use letters, digits, and underscores")
	}
	snake := strings.Join(words, "_")
	switch {
	case snake[0] >= '0' && snake[0] <= '9':
		return Name{}, nameError(s, "it starts with a digit")
	case len(snake) > 50:
		return Name{}, nameError(s, "it is longer than 50 characters")
	case reserved[snake]:
		return Name{}, nameError(s, fmt.Sprintf("%q is reserved by the jobs package", snake))
	case strings.HasSuffix(snake, "_test") || strings.HasSuffix(snake, "_gen"):
		return Name{}, nameError(s, "a name that ends in _test or _gen is reserved for Go files")
	case hasBuildSelectorSuffix(words):
		return Name{}, nameError(s, "a name that ends in a GOOS or GOARCH value is reserved for Go files")
	}
	var pascal strings.Builder
	for _, w := range words {
		pascal.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return Name{Snake: snake, Pascal: pascal.String()}, nil
}

func hasBuildSelectorSuffix(words []string) bool {
	last := words[len(words)-1]
	return knownGOOS[last] || knownGOARCH[last]
}

func nameError(s, why string) error {
	return fmt.Errorf("invalid job name %q: %s\nTry: spinup g job send_digest", s, why)
}

// splitWords splits s at underscores, hyphens, and lower-to-upper case
// changes, and lowercases the words.
func splitWords(s string) ([]string, bool) {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-':
			flush()
		case r > unicode.MaxASCII || !unicode.IsLetter(r) && !unicode.IsDigit(r):
			return nil, false
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1])):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words, true
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if in.Lock == nil || !modules.Enabled(&project.Config{Modules: in.Lock.Packs}, pack) {
		return nil, fmt.Errorf("cannot generate a job: the app has no jobs module (the jobs pack is not in spinup.lock)\n" +
			"Try: spinup module add jobs")
	}
	if len(in.Args) != 1 {
		return nil, fmt.Errorf("cannot generate a job: expected one job name, got %d arguments\nTry: spinup g job send_digest", len(in.Args))
	}
	name, err := ParseName(in.Args[0])
	if err != nil {
		return nil, err
	}
	module, _ := in.Lock.Answers["module"].(string)
	if module == "" {
		return nil, fmt.Errorf("cannot generate a job: spinup.lock has no module answer\nTry: spinup doctor")
	}
	data := map[string]string{"Snake": name.Snake, "Pascal": name.Pascal, "Module": module}
	jobFile, err := render(jobTemplate, data)
	if err != nil {
		return nil, err
	}
	testFile, err := render(testTemplate, data)
	if err != nil {
		return nil, err
	}
	jobPath := path.Join(Dir, name.Snake+".go")
	dir := filepath.Join(in.Root, filepath.FromSlash(Dir))
	existing, err := scan(dir, nil)
	if err != nil {
		return nil, err
	}
	if existing.hasDeclarationOutside(name.Pascal, name.Snake+".go") {
		return nil, fmt.Errorf("cannot generate job %q: %s is already declared in %s\nTry: use a different job name", name.Snake, name.Pascal, Dir)
	}
	found, err := Scan(dir, map[string][]byte{name.Snake + ".go": jobFile})
	if err != nil {
		return nil, err
	}
	register, err := RegisterSource(found)
	if err != nil {
		return nil, err
	}
	var p vfs.Plan
	for _, f := range []vfs.File{
		{Path: jobPath, Data: jobFile, Mode: 0o644, Class: string(manifest.Scaffold)},
		{Path: path.Join(Dir, name.Snake+"_test.go"), Data: testFile, Mode: 0o644, Class: string(manifest.Scaffold)},
		{Path: RegisterFile, Data: register, Mode: 0o644, Class: string(manifest.Managed)},
	} {
		if err := p.Add(f); err != nil {
			return nil, err
		}
	}
	return &gen.Output{
		Plan:      p,
		PostSteps: []gen.PostStep{gen.PostGofmt},
		Next: []string{
			"cd api && go test ./internal/jobs/...",
			"cd api && go run ./cmd/worker",
		},
	}, nil
}

// Scan finds the jobs in the Go files of dir. A job is a type named <X>Args
// with a Kind method, and a top-level function named <X>. The files in
// extra (base name to content) replace or add files before the scan. It
// skips test files and register_gen.go. It returns the <X> names, sorted.
func Scan(dir string, extra map[string][]byte) ([]string, error) {
	result, err := scan(dir, extra)
	if err != nil {
		return nil, err
	}
	return result.jobs, nil
}

type scanResult struct {
	jobs         []string
	args         map[string]bool
	funcs        map[string]bool
	declarations map[string]map[string]bool
}

func (r scanResult) hasDeclarationOutside(base, allowedFile string) bool {
	if !r.args[base] && !r.funcs[base] {
		return false
	}
	for file := range r.declarations[base] {
		if file != allowedFile {
			return true
		}
	}
	return false
}

func scan(dir string, extra map[string][]byte) (scanResult, error) {
	sources := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return scanResult{}, fmt.Errorf("read %s: %w\nTry: check the directory permissions", Dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return scanResult{}, fmt.Errorf("read %s/%s: %w\nTry: check the file permissions", Dir, e.Name(), err)
		}
		sources[e.Name()] = data
	}
	for name, data := range extra {
		sources[name] = data
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") && name != path.Base(RegisterFile) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	fset := token.NewFileSet()
	args := map[string]bool{}
	kinds := map[string]bool{}
	funcs := map[string]bool{}
	declarations := map[string]map[string]bool{}
	for _, name := range names {
		file, err := parser.ParseFile(fset, path.Join(Dir, name), sources[name], parser.SkipObjectResolution)
		if err != nil {
			return scanResult{}, fmt.Errorf("parse %s/%s: %w\nTry: fix the Go syntax error, then run spinup g job again", Dir, name, err)
		}
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.TYPE {
				for _, spec := range gen.Specs {
					typ, ok := spec.(*ast.TypeSpec)
					if !ok || !strings.HasSuffix(typ.Name.Name, "Args") || len(typ.Name.Name) == len("Args") {
						continue
					}
					base := strings.TrimSuffix(typ.Name.Name, "Args")
					args[base] = true
					if declarations[base] == nil {
						declarations[base] = map[string]bool{}
					}
					declarations[base][name] = true
				}
				continue
			}
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Recv == nil {
				funcs[fn.Name.Name] = true
				if declarations[fn.Name.Name] == nil {
					declarations[fn.Name.Name] = map[string]bool{}
				}
				declarations[fn.Name.Name][name] = true
				continue
			}
			if fn.Name.Name != "Kind" || len(fn.Recv.List) != 1 || fn.Type.Params.NumFields() != 0 {
				continue
			}
			if typ := receiverType(fn.Recv.List[0].Type); strings.HasSuffix(typ, "Args") && len(typ) > len("Args") {
				kinds[strings.TrimSuffix(typ, "Args")] = true
			}
		}
	}
	var jobs []string
	for base := range kinds {
		if funcs[base] {
			jobs = append(jobs, base)
		}
	}
	sort.Strings(jobs)
	return scanResult{jobs: jobs, args: args, funcs: funcs, declarations: declarations}, nil
}

func receiverType(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// RegisterSource returns register_gen.go for the jobs in found.
func RegisterSource(found []string) ([]byte, error) {
	return render(registerTemplate, map[string]any{"Jobs": found})
}

func render(tmpl *template.Template, data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render %s: %w\nTry: report this as a spinup bug", tmpl.Name(), err)
	}
	out, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format %s: %w\nTry: report this as a spinup bug", tmpl.Name(), err)
	}
	return out, nil
}

var registerTemplate = template.Must(template.New("register_gen.go").Parse(`// Code generated by spinup g job. DO NOT EDIT.

package jobs

import "github.com/stopyellingatme/spinup/kit/jobs"

func init() { generated = registerGenerated }

// registerGenerated registers every job of this package, sorted by name.
// spinup g job rewrites this file from the <Name>Args types and <Name>
// functions it finds in the package.
func registerGenerated(w *jobs.Worker, d Deps) {
{{- range .Jobs }}
	jobs.Register(w, {{ . }}(d))
{{- end }}
}
`))

var jobTemplate = template.Must(template.New("job.go").Parse(`package jobs

import (
	"context"
	"log/slog"

	"github.com/stopyellingatme/spinup/kit/jobs"
)

// {{ .Pascal }}Args are the arguments of a {{ .Snake }} job. River stores them as
// JSON, so add exported fields with json tags.
type {{ .Pascal }}Args struct{}

// Kind names the job kind in River. Do not change it after jobs exist.
func ({{ .Pascal }}Args) Kind() string { return "{{ .Snake }}" }

// {{ .Pascal }} returns the function that works {{ .Snake }} jobs. Return an
// error to retry the job later.
//
// Insert a job from the API with a jobs.Client:
//
//	client.Insert(ctx, jobsapp.{{ .Pascal }}Args{})
func {{ .Pascal }}(d Deps) func(ctx context.Context, job jobs.Job[{{ .Pascal }}Args]) error {
	return func(ctx context.Context, job jobs.Job[{{ .Pascal }}Args]) error {
		slog.InfoContext(ctx, "job {{ .Snake }}", "job_id", job.ID, "attempt", job.Attempt)
		return nil
	}
}
`))

var testTemplate = template.Must(template.New("job_test.go").Parse(`package jobs

import (
	"context"
	"testing"

	"{{ .Module }}/api/db/migrations"
	"github.com/stopyellingatme/spinup/kit/jobs"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func Test{{ .Pascal }}(t *testing.T) {
	if got := ({{ .Pascal }}Args{}).Kind(); got != "{{ .Snake }}" {
		t.Fatalf("Kind() = %q, want {{ .Snake }}", got)
	}
	pool := testkit.DBWithMigrations(t, migrations.FS)
	work := {{ .Pascal }}(Deps{DB: pool})
	job := jobs.Job[{{ .Pascal }}Args]{ID: 1, Args: {{ .Pascal }}Args{}, Attempt: 1}
	if err := work(context.Background(), job); err != nil {
		t.Fatal(err)
	}
}
`))
