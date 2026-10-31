package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ci"
	spexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestDetectLang(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{"rust", []string{"Cargo.toml"}, "rust"},
		{"python", []string{"pyproject.toml", "uv.lock"}, "python"},
		{"python without lock", []string{"pyproject.toml"}, "none"},
		{"lock without python", []string{"uv.lock"}, "none"},
		{"node", []string{"package.json"}, "node"},
		{"rust precedence", []string{"Cargo.toml", "pyproject.toml", "uv.lock", "package.json"}, "rust"},
		{"python precedence", []string{"pyproject.toml", "uv.lock", "package.json"}, "python"},
		{"incomplete python and node", []string{"pyproject.toml", "package.json"}, "node"},
		{"unknown", []string{"Dockerfile"}, "none"},
		{"empty", nil, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, name := range tc.files {
				writeServiceFile(t, filepath.Join(dir, name), "")
			}
			if got := ci.DetectLang(dir); got != tc.want {
				t.Fatalf("DetectLang = %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("manifest directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "Cargo.toml"), 0755); err != nil {
			t.Fatal(err)
		}
		if got := ci.DetectLang(dir); got != "none" {
			t.Fatalf("DetectLang = %q, want none", got)
		}
	})
}

func TestServicesMatrix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{"declaration order", serviceTable("zebra", 8101) + serviceTable("alpha", 8102) + serviceTable("nodeapp", 8103) + serviceTable("custom", 8104), `{"include":[{"name":"zebra","lang":"rust"},{"name":"alpha","lang":"python"},{"name":"nodeapp","lang":"node"},{"name":"custom","lang":"none"}]}`},
		{"no services", "", `{"include":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := serviceProject(t, tc.config)
			for _, name := range []string{"zebra/Cargo.toml", "alpha/pyproject.toml", "alpha/uv.lock", "nodeapp/package.json", "undeclared/Cargo.toml"} {
				writeServiceFile(t, filepath.Join(root, "services", name), "")
			}
			doc, err := ci.Matrix(root)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.want {
				t.Fatalf("matrix = %s, want %s", data, tc.want)
			}
		})
	}
}

func TestServiceDockerfileGolden(t *testing.T) {
	t.Parallel()
	stack := stackversions.MustLoad()
	for _, lang := range []string{"rust", "python", "node"} {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			got, err := ci.TestDockerfile(lang, stack)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", "services", lang+".Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("Dockerfile differs from golden:\n%s", got)
			}
		})
	}
}

func TestServiceDockerfileUsesSuppliedPins(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		lang   string
		images []string
	}{
		{"rust", []string{"rust"}},
		{"python", []string{"python", "uv"}},
		{"node", []string{"node"}},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			t.Parallel()
			stack := stackversions.MustLoad()
			for _, name := range tc.images {
				image := stack.Images[name]
				image.Tag = "custom"
				stack.Images[name] = image
			}
			stack.NPM["pnpm"] = "99.1.2"
			got, err := ci.TestDockerfile(tc.lang, stack)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.images {
				if !strings.Contains(got, stack.Images[name].Ref()) {
					t.Fatalf("Dockerfile does not use the supplied %s pin:\n%s", name, got)
				}
			}
			if tc.lang == "node" && !strings.Contains(got, "pnpm@99.1.2") {
				t.Fatalf("Dockerfile does not use the supplied pnpm pin:\n%s", got)
			}
		})
	}
}

func TestServiceDockerfileErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, lang, missingImage, want string
		nilStack, missingPNPM          bool
	}{
		{name: "missing stack", lang: "rust", nilStack: true, want: "stack pins are missing"},
		{name: "unsupported", lang: "go", want: "no test Dockerfile"},
		{name: "none", lang: "none", want: "no test Dockerfile"},
		{name: "rust image", lang: "rust", missingImage: "rust", want: "rust"},
		{name: "python image", lang: "python", missingImage: "python", want: "python"},
		{name: "uv image", lang: "python", missingImage: "uv", want: "uv"},
		{name: "node image", lang: "node", missingImage: "node", want: "node"},
		{name: "pnpm version", lang: "node", missingPNPM: true, want: "pnpm pin is missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stack := stackversions.MustLoad()
			delete(stack.Images, tc.missingImage)
			if tc.missingPNPM {
				delete(stack.NPM, "pnpm")
			}
			if tc.nilStack {
				stack = nil
			}
			got, err := ci.TestDockerfile(tc.lang, stack)
			assertServiceError(t, err, tc.want)
			if got != "" {
				t.Fatalf("invalid Dockerfile = %q", got)
			}
		})
	}
}

func TestServiceConfigErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, content, want string }{
		{"missing", "", "spinup.toml"},
		{"malformed", "[[services]", "parse spinup.toml"},
		{"invalid name", serviceTable("../outside", 8101), "service name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tc.content != "" {
				writeServiceFile(t, filepath.Join(root, "spinup.toml"), tc.content)
			}
			_, err := ci.Matrix(root)
			assertServiceError(t, err, tc.want)
			runner := new(spexec.Fake)
			err = ci.TestService(context.Background(), runner, root, "../outside", ci.TestServiceOptions{})
			assertServiceError(t, err, tc.want)
			if len(runner.Calls()) != 0 {
				t.Fatal("invalid config executed Docker")
			}
		})
	}
}

func TestRunServiceTests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		lang, manifest string
	}{
		{"rust", "Cargo.toml"},
		{"python", "pyproject.toml"},
		{"node", "package.json"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			t.Parallel()
			root := serviceProject(t, serviceTable("ocr", 8101))
			dir := filepath.Join(root, "services", "ocr")
			writeServiceFile(t, filepath.Join(dir, tc.manifest), "original manifest\n")
			if tc.lang == "python" {
				writeServiceFile(t, filepath.Join(dir, "uv.lock"), "original lock\n")
			}
			writeServiceFile(t, filepath.Join(dir, "Dockerfile"), "original Dockerfile\n")
			before := serviceTree(t, root)
			var out, errOut bytes.Buffer
			runner := new(spexec.Fake)
			runner.Expect("docker", "build", "-f", "-", "services/ocr").Return("test output\n", "build output\n", 0)
			if err := ci.TestService(context.Background(), runner, root, "ocr", ci.TestServiceOptions{Out: &out, ErrOut: &errOut}); err != nil {
				t.Fatal(err)
			}
			runner.AssertDone(t)
			calls := runner.Calls()
			if len(calls) != 1 || calls[0].Dir != root {
				t.Fatalf("unexpected calls: %+v", calls)
			}
			data, err := io.ReadAll(calls[0].Stdin)
			if err != nil {
				t.Fatal(err)
			}
			want, err := ci.TestDockerfile(tc.lang, stackversions.MustLoad())
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != want {
				t.Fatalf("stdin = %q, want %q", data, want)
			}
			if out.String() != "test output\n" || errOut.String() != "build output\n" {
				t.Fatalf("output = %q, stderr = %q", &out, &errOut)
			}
			if after := serviceTree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("project changed: before %v, after %v", before, after)
			}
		})
	}
}

func TestServicePreviewAndNoTests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		dryRun   bool
		manifest string
	}{
		{"preview", true, "Cargo.toml"},
		{"custom service", false, "Dockerfile"},
		{"custom preview", true, "Dockerfile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := serviceProject(t, serviceTable("ocr", 8101))
			writeServiceFile(t, filepath.Join(root, "services", "ocr", tc.manifest), "unchanged")
			before := serviceTree(t, root)
			var out bytes.Buffer
			runner := new(spexec.Fake)
			if err := ci.TestService(context.Background(), runner, root, "ocr", ci.TestServiceOptions{DryRun: tc.dryRun, Out: &out}); err != nil {
				t.Fatal(err)
			}
			if len(runner.Calls()) != 0 {
				t.Fatal("preview or unrecognized service executed Docker")
			}
			if tc.manifest == "Cargo.toml" {
				want, err := ci.TestDockerfile("rust", stackversions.MustLoad())
				if err != nil {
					t.Fatal(err)
				}
				if out.String() != want {
					t.Fatalf("preview = %q, want %q", &out, want)
				}
			} else if !strings.Contains(out.String(), "no tests detected") {
				t.Fatalf("missing note: %q", &out)
			}
			if after := serviceTree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("preview or unrecognized service changed the project")
			}
		})
	}
}

func TestServiceErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, service, manifest, want string
		runErr                        error
	}{
		{name: "unknown", service: "unknown", want: "not declared"},
		{name: "traversal", service: "../../outside", want: "not declared"},
		{name: "missing directory", service: "ocr", want: "directory"},
		{name: "directory is file", service: "ocr", manifest: "file", want: "not a directory"},
		{name: "docker failed", service: "ocr", manifest: "Cargo.toml", runErr: errors.New("docker unavailable"), want: "docker unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := serviceProject(t, serviceTable("ocr", 8101))
			if tc.manifest == "file" {
				writeServiceFile(t, filepath.Join(root, "services", "ocr"), "not a directory")
			} else if tc.manifest != "" {
				writeServiceFile(t, filepath.Join(root, "services", "ocr", tc.manifest), "")
			}
			runner := new(spexec.Fake)
			if tc.runErr != nil {
				runner.Expect("docker").Fail(tc.runErr)
			}
			err := ci.TestService(context.Background(), runner, root, tc.service, ci.TestServiceOptions{})
			assertServiceError(t, err, tc.want)
			if tc.runErr != nil && !errors.Is(err, tc.runErr) {
				t.Fatalf("runner cause lost: %v", err)
			}
			if tc.runErr == nil && len(runner.Calls()) != 0 {
				t.Fatal("invalid service executed Docker")
			}
			runner.AssertDone(t)
		})
	}
}

func assertServiceError(t *testing.T, err error, want string) {
	t.Helper()
	var ue *ui.Error
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), want) || ue.Try == "" {
		t.Fatalf("error = %v, want ui.Error containing %q and a Try hint", err, want)
	}
	var out bytes.Buffer
	ui.New(nil, io.Discard, &out, ui.Options{NoColor: true}).RenderError(err)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "Try:") {
		t.Fatalf("error does not end with a Try line: %q", &out)
	}
}

func serviceTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func writeServiceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func serviceProject(t *testing.T, tables string) string {
	t.Helper()
	root := t.TempDir()
	writeServiceFile(t, filepath.Join(root, "spinup.toml"), "[app]\nname = \"demo\"\nmodule = \"example.com/demo\"\n"+tables)
	return root
}

func serviceTable(name string, port int) string {
	return "\n[[services]]\nname = " + strconv.Quote(name) + "\nport = 8080\ndev_port = " + strconv.Itoa(port) + "\nhealth = \"/healthz\"\n"
}
