package service_test

import (
	"bytes"
	"io/fs"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/services"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

// pdfFiles is the file list of a PDF service (ADR 0026 decision 1.5).
var pdfFiles = []string{
	".dockerignore",
	"Dockerfile",
	"README.md",
	"openapi.yaml",
	"pyproject.toml",
	"service/__init__.py",
	"service/assets/report.css",
	"service/fetcher.py",
	"service/healthcheck.py",
	"service/limits.py",
	"service/main.py",
	"service/render.py",
	"service/render_job.py",
	"service/telemetry.py",
	"service/templates/sample.html.j2",
	"tests/nosocket/sitecustomize.py",
	"tests/test_main.py",
	"tests/test_render.py",
	"tests/test_telemetry.py",
	"uv.lock",
}

// pdfOverlay lists the files that the PDF tree replaces or adds, by the source name.
var pdfOverlay = []string{
	"pyproject.toml", "uv.lock", "openapi.yaml",
	"service/main.py", "service/render.py", "service/render_job.py", "service/fetcher.py", "service/limits.py",
	"service/assets/report.css", "service/templates/sample.html.j2",
	"tests/test_main.py", "tests/test_render.py", "tests/nosocket/sitecustomize.py",
}

func TestPDFKind(t *testing.T) {
	t.Parallel()
	in := input(t, config, "reports", "--lang", "python", "--kind", "pdf")
	out, err := generator(t).Run(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	dir := services.ContextDir("reports")
	var got []string
	for _, file := range out.Plan.Files {
		if file.Path == project.ConfigFile {
			continue
		}
		rel := strings.TrimPrefix(file.Path, dir+"/")
		if path.Base(rel) == ".gitkeep" {
			t.Errorf("plan holds the placeholder %s", file.Path)
		}
		if file.Class != "scaffold" || file.Mode != 0o644 {
			t.Errorf("file = %s %s %o", file.Path, file.Class, file.Mode)
		}
		got = append(got, rel)
	}
	sorted := slices.Clone(got)
	slices.Sort(sorted)
	if len(slices.Compact(slices.Clone(sorted))) != len(got) {
		t.Fatalf("plan has a duplicate path: %v", got)
	}
	if !reflect.DeepEqual(sorted, pdfFiles) {
		t.Fatalf("files = %v\nwant %v", sorted, pdfFiles)
	}
	files := out.Plan.FS()
	for _, name := range pdfOverlay {
		data, err := fs.ReadFile(files, dir+"/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, templateFile(t, "python-pdf", name)) {
			t.Errorf("%s is not the PDF template file", name)
		}
	}
	for _, name := range []string{".dockerignore", "service/__init__.py", "service/telemetry.py", "tests/test_telemetry.py"} {
		data, err := fs.ReadFile(files, dir+"/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, templateFile(t, "python", name)) {
			t.Errorf("%s is not the base template file", name)
		}
	}
	data, err := fs.ReadFile(files, project.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	svc := cfg.Services[0]
	if svc.Memory != "3g" || svc.TmpSize != "256m" || svc.Public != "" || svc.OpenAPI != "openapi.yaml" {
		t.Fatalf("service = %+v", svc)
	}
	if !reflect.DeepEqual(svc.Healthcheck, []string{"/app/.venv/bin/python", "-m", "service.healthcheck"}) {
		t.Fatalf("healthcheck = %v", svc.Healthcheck)
	}
	if !reflect.DeepEqual(out.Next, []string{"spinup g client", "spinup dev"}) {
		t.Fatalf("next = %v", out.Next)
	}
}

func TestPDFKindDeterministic(t *testing.T) {
	t.Parallel()
	in := input(t, config, "reports", "--lang", "python", "--kind", "pdf", "--no-openapi")
	first, err := generator(t).Run(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generator(t).Run(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Plan.Files, second.Plan.Files) {
		t.Fatal("two renders differ")
	}
	if _, ok := first.Plan.Get(services.ContextDir("reports") + "/openapi.yaml"); ok {
		t.Fatal("--no-openapi kept openapi.yaml")
	}
}

func TestServiceKindUnchanged(t *testing.T) {
	t.Parallel()
	plain, err := generator(t).Run(t.Context(), input(t, config, "ocr", "--lang", "python"))
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := generator(t).Run(t.Context(), input(t, config, "ocr", "--lang", "python", "--kind", "service"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain.Plan.Files, explicit.Plan.Files) {
		t.Fatal("--kind service changed the output")
	}
	data, err := fs.ReadFile(plain.Plan.FS(), project.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "memory") || strings.Contains(string(data), "tmp_size") {
		t.Fatalf("a plain service has deploy limits:\n%s", data)
	}
}

func TestKindErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown kind", []string{"reports", "--lang", "python", "--kind", "word"}, "--kind must be service or pdf"},
		{"empty kind", []string{"reports", "--lang", "python", "--kind="}, "--kind must be service or pdf"},
		{"rust", []string{"reports", "--lang", "rust", "--kind", "pdf"}, "--kind pdf needs --lang python"},
		{"node", []string{"reports", "--lang", "node", "--kind", "pdf"}, "--kind pdf needs --lang python"},
		{"plugin", []string{"reports", "--lang", "acme/report", "--kind", "pdf"}, "--kind pdf needs --lang python"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := generator(t).Run(t.Context(), input(t, config, tc.args...))
			if out != nil || err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "\nTry: ") {
				t.Fatalf("out = %v, error = %v, want %q and a Try line", out, err, tc.want)
			}
		})
	}
}

func TestPDFDockerfile(t *testing.T) {
	t.Parallel()
	stack, err := stackversions.Load()
	if err != nil {
		t.Fatal(err)
	}
	out, err := generator(t).Run(t.Context(), input(t, config, "reports", "--lang", "python", "--kind", "pdf", "--port", "9001"))
	if err != nil {
		t.Fatal(err)
	}
	docker, err := fs.ReadFile(out.Plan.FS(), services.Dockerfile("reports"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"FROM " + stack.Images["python"].Ref() + "\n",
		"COPY --from=" + stack.Images["uv"].Ref() + " /uv /bin/uv",
		"apt-get install -y --no-install-recommends libpango-1.0-0 libpangoft2-1.0-0 libharfbuzz-subset0 fontconfig fonts-dejavu-core fonts-liberation",
		"rm -rf /var/lib/apt/lists/*",
		"fc-cache -f",
		"uv sync --locked --no-dev --no-build",
		"useradd --uid 10001",
		"USER service",
		"HOME=/tmp", "XDG_CACHE_HOME=/tmp/cache",
		"ENV PORT=9001", "EXPOSE 9001", `"--port", "9001"`,
	} {
		if !strings.Contains(string(docker), fragment) {
			t.Errorf("Dockerfile does not contain %q", fragment)
		}
	}
	if strings.Count(string(docker), "FROM ") != 1 {
		t.Error("the PDF image must use only the pinned python image")
	}
	// Only the .tmpl sources go through text/template. Jinja2 files and their tests use {{ }} on purpose.
	for _, name := range []string{"Dockerfile", "README.md", "service/healthcheck.py"} {
		data, err := fs.ReadFile(out.Plan.FS(), services.ContextDir("reports")+"/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "{{") {
			t.Errorf("generated file %s contains an unexpanded template action", name)
		}
	}
}

func TestPDFTemplatePins(t *testing.T) {
	t.Parallel()
	stack, err := stackversions.Load()
	if err != nil {
		t.Fatal(err)
	}
	var python struct {
		Project struct {
			Dependencies []string `toml:"dependencies"`
		} `toml:"project"`
		Groups struct {
			Dev []string `toml:"dev"`
		} `toml:"dependency-groups"`
	}
	if err := toml.Unmarshal(templateFile(t, "python-pdf", "pyproject.toml"), &python); err != nil {
		t.Fatal(err)
	}
	deps := slices.Concat(python.Project.Dependencies, python.Groups.Dev)
	if got, want := len(deps), len(stack.PythonPackages)+len(stack.PythonPDFPackages); got != want {
		t.Fatalf("PDF dependency count = %d, want %d", got, want)
	}
	for _, pins := range []map[string]string{stack.PythonPackages, stack.PythonPDFPackages} {
		for name, version := range pins {
			if !slices.Contains(deps, name+"=="+version) {
				t.Errorf("PDF dependency %s==%s is missing", name, version)
			}
		}
	}
	for _, name := range []string{"weasyprint", "jinja2", "pypdf"} {
		if stack.PythonPDFPackages[name] == "" {
			t.Errorf("PDF package %s is missing", name)
		}
	}
	if !slices.Contains(python.Groups.Dev, "pypdf=="+stack.PythonPDFPackages["pypdf"]) {
		t.Error("pypdf must be a dev dependency")
	}
	lock := string(templateFile(t, "python-pdf", "uv.lock"))
	for name, version := range stack.PythonPDFPackages {
		if !strings.Contains(lock, "name = \""+name+"\"\nversion = \""+version+"\"") {
			t.Errorf("uv.lock does not lock %s %s", name, version)
		}
	}
}

func TestTemplateOpenAPI(t *testing.T) {
	t.Parallel()
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string                    `yaml:"operationId"`
			Responses   map[string]map[string]any `yaml:"responses"`
		} `yaml:"paths"`
		Components struct {
			Schemas map[string]struct {
				Required []string `yaml:"required"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(templateFile(t, "python-pdf", "openapi.yaml"), &spec); err != nil {
		t.Fatal(err)
	}
	render := spec.Paths["/v1/render"]["post"]
	if render.OperationID != "render" {
		t.Fatalf("operationId = %q, want render", render.OperationID)
	}
	if _, ok := spec.Paths["/v1/sum"]; ok {
		t.Fatal("the PDF API must not have /v1/sum")
	}
	if spec.Paths["/healthz"]["get"].OperationID != "healthz" {
		t.Fatal("the PDF API must keep /healthz")
	}
	ok := render.Responses["200"]
	content, _ := ok["content"].(map[string]any)
	if _, found := content["application/pdf"]; !found {
		t.Fatalf("200 response = %v, want application/pdf", ok)
	}
	if got := spec.Components.Schemas["RenderRequest"].Required; !reflect.DeepEqual(got, []string{"template", "data"}) {
		t.Fatalf("RenderRequest required = %v", got)
	}
	if _, found := spec.Components.Schemas["Problem"]; !found {
		t.Fatal("the PDF API must declare Problem")
	}
}
