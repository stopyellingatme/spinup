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
	"slices"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
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
	}
	var pascal strings.Builder
	for _, w := range words {
		pascal.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return Name{Snake: snake, Pascal: pascal.String()}, nil
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
	if in.Lock == nil || !slices.Contains(in.Lock.Packs, pack) {
		return nil, fmt.Errorf("cannot generate a job: the app has no jobs module (the jobs pack is not in spinup.lock)\n" +
			"Try: add the jobs module: create the app with --modules ci,jobs (spinup module add arrives in a later version)")
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
	found, err := Scan(filepath.Join(in.Root, filepath.FromSlash(Dir)), map[string][]byte{name.Snake + ".go": jobFile})
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
	sources := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w\nTry: check the directory permissions", Dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s/%s: %w\nTry: check the file permissions", Dir, e.Name(), err)
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
	kinds := map[string]bool{}
	funcs := map[string]bool{}
	for _, name := range names {
		file, err := parser.ParseFile(fset, path.Join(Dir, name), sources[name], parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s/%s: %w\nTry: fix the Go syntax error, then run spinup g job again", Dir, name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Recv == nil {
				funcs[fn.Name.Name] = true
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
	var found []string
	for base := range kinds {
		if funcs[base] {
			found = append(found, base)
		}
	}
	sort.Strings(found)
	return found, nil
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
