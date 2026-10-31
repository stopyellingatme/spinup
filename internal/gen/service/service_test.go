package service_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	_ "github.com/stopyellingatme/spinup/internal/gen/service"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/services"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func generator(t *testing.T) gen.Generator {
	t.Helper()
	g, ok := registry.Lookup("service")
	if !ok {
		t.Fatal("service generator is not registered")
	}
	return g
}

func input(t *testing.T, config string, args ...string) gen.Input {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := pflag.NewFlagSet("service", pflag.ContinueOnError)
	generator(t).Flags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return gen.Input{Root: root, Args: fs.Args(), Flags: fs}
}

func TestRequiredLanguage(t *testing.T) {
	t.Parallel()
	in := input(t, "", "ocr")
	_, err := generator(t).Run(t.Context(), in)
	if err == nil || !strings.Contains(err.Error(), "--lang") || !strings.Contains(err.Error(), "\nTry:") {
		t.Fatalf("error = %v, want --lang and a Try line", err)
	}
}

const config = `# Preserve this comment and spacing.
behavior_version = 1

[app]
name = "todo"
module = "example.com/todo"

[custom]
value = 'keep me'
`

func TestPlan(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		flags         []string
		port, devPort int
		noOpenAPI     bool
		healthcheck   []string
	}{
		{"rust", []string{"--lang", "rust"}, 8080, 8101, false, []string{"/service", "healthcheck"}},
		{"python", []string{"--lang", "python"}, 8080, 8101, false, []string{"/app/.venv/bin/python", "-m", "service.healthcheck"}},
		{"node", []string{"--lang", "node"}, 8080, 8101, false, []string{"/usr/local/bin/node", "/app/src/healthcheck.ts"}},
		{"custom ports", []string{"--lang", "rust", "--port", "9000", "--dev-port", "8200"}, 9000, 8200, false, []string{"/service", "healthcheck"}},
		{"no OpenAPI", []string{"--lang", "python", "--no-openapi"}, 8080, 8101, true, []string{"/app/.venv/bin/python", "-m", "service.healthcheck"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := input(t, config, append([]string{"ocr"}, tc.flags...)...)
			out, err := generator(t).Run(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			files := out.Plan.FS()
			if !reflect.DeepEqual(out.Edits, []string{"spinup.toml"}) {
				t.Fatalf("edits = %v", out.Edits)
			}
			next := []string{"spinup g client", "spinup dev"}
			if tc.noOpenAPI {
				next = next[1:]
			}
			if !reflect.DeepEqual(out.Next, next) {
				t.Fatalf("next = %v", out.Next)
			}
			if len(out.PostSteps) != 0 || len(out.Commands) != 0 {
				t.Fatal("service generation must not run external tools")
			}
			for _, f := range out.Plan.Files {
				if f.Class != "scaffold" || f.Mode != 0o644 {
					t.Errorf("file = %+v", f)
				}
			}
			data, err := fs.ReadFile(files, "spinup.toml")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(data), config+"\n[[services]]\n") {
				t.Fatalf("config changed: %s", data)
			}
			cfg, err := project.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			svc := cfg.Services[0]
			if svc.Name != "ocr" || svc.Port != tc.port || svc.DevPort != tc.devPort || svc.Health != "/healthz" {
				t.Fatalf("service = %+v", svc)
			}
			_, err = fs.Stat(files, services.ContextDir("ocr")+"/openapi.yaml")
			if tc.noOpenAPI {
				if svc.OpenAPI != "" || !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("OpenAPI = %q, stat = %v", svc.OpenAPI, err)
				}
			} else if err != nil || svc.OpenAPI != "openapi.yaml" {
				t.Fatalf("OpenAPI = %q, stat = %v", svc.OpenAPI, err)
			}
			if !reflect.DeepEqual(svc.Healthcheck, tc.healthcheck) {
				t.Fatalf("healthcheck = %v", svc.Healthcheck)
			}
			original, err := os.ReadFile(filepath.Join(in.Root, "spinup.toml"))
			if err != nil || string(original) != config {
				t.Fatalf("generator wrote config: %s, %v", original, err)
			}
			if _, err := os.Stat(filepath.Join(in.Root, "services")); !os.IsNotExist(err) {
				t.Fatalf("generator wrote files: %v", err)
			}
		})
	}
}

func TestDefaultDevPort(t *testing.T) {
	t.Parallel()
	for _, ending := range []string{"", "\n", "\n\n"} {
		t.Run(fmt.Sprintf("newlines-%d", len(ending)), func(t *testing.T) {
			t.Parallel()
			src := config + `\n[[services]]\nname = "first"\nport = 8080\ndev_port = 8101\nhealth = "/healthz"\n\n[[services]]\nname = "third"\nport = 8080\ndev_port = 8103\nhealth = "/healthz"`
			src = strings.ReplaceAll(src, `\n`, "\n") + ending
			in := input(t, src, "ocr", "--lang", "rust")
			out, err := generator(t).Run(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			data, err := fs.ReadFile(out.Plan.FS(), "spinup.toml")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(data), src) {
				t.Fatal("existing bytes changed")
			}
			cfg, err := project.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Services[2].DevPort != 8102 {
				t.Fatalf("dev port = %d", cfg.Services[2].DevPort)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		args    []string
		config  string
		prepare func(*testing.T, string)
		want    string
	}{
		{name: "missing name", args: []string{"--lang", "rust"}, want: "expected one name"},
		{name: "extra name", args: []string{"ocr", "more", "--lang", "rust"}, want: "expected one name"},
		{name: "reserved", args: []string{"api", "--lang", "rust"}, want: "reserved"},
		{name: "invalid", args: []string{"../ocr", "--lang", "rust"}, want: "must match"},
		{name: "unsupported", args: []string{"ocr", "--lang", "go"}, want: "rust, python, or node"},
		{name: "empty public", args: []string{"ocr", "--lang", "node", "--public="}, want: "--public requires"},
		{name: "bad public", args: []string{"ocr", "--lang", "node", "--public", "svc/ocr"}, want: "must be a path"},
		{name: "session needs auth", args: []string{"ocr", "--lang", "node", "--public", "/svc/ocr", "--auth", "session"}, want: "needs the auth module"},
		{name: "empty language", args: []string{"ocr", "--lang="}, want: "--lang"},
		{name: "invalid port", args: []string{"ocr", "--lang", "rust", "--port", "0"}, want: "port 0"},
		{name: "large port", args: []string{"ocr", "--lang", "rust", "--port", "65536"}, want: "65536"},
		{name: "explicit zero", args: []string{"ocr", "--lang", "rust", "--dev-port", "0"}, want: "dev_port 0"},
		{name: "reserved port", args: []string{"ocr", "--lang", "rust", "--dev-port", "8080"}, want: "used by the app"},
		{name: "duplicate port", args: []string{"ocr", "--lang", "rust", "--dev-port", "8101"}, config: "[[services]]\nname='other'\nport=8080\ndev_port=8101\nhealth='/healthz'\n", want: "also used"},
		{name: "declared", args: []string{"ocr", "--lang", "rust"}, config: "[[services]]\nname='ocr'\nport=8080\ndev_port=8101\nhealth='/healthz'\n", want: "already declares"},
		{name: "bad TOML", args: []string{"ocr", "--lang", "rust"}, config: "[broken", want: "spinup.toml"},
		{name: "directory exists", args: []string{"ocr", "--lang", "rust"}, prepare: func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "services", "ocr"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, want: "already exists"},
		{name: "file exists", args: []string{"ocr", "--lang", "python"}, prepare: func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "services"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "services", "ocr"), []byte("keep"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, want: "already exists"},
		{name: "missing config", args: []string{"ocr", "--lang", "rust"}, prepare: func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "spinup.toml")); err != nil {
				t.Fatal(err)
			}
		}, want: "read spinup.toml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := input(t, tc.config, tc.args...)
			if tc.prepare != nil {
				tc.prepare(t, in.Root)
			}
			out, err := generator(t).Run(t.Context(), in)
			if out != nil || err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("out = %v, error = %v, want %q and Try:", out, err, tc.want)
			}
		})
	}
}

func TestGoldenService(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{"rust", "python", "node"} {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			testutil.GoldenSelected(t, "service-"+lang)
			in := input(t, config, "ocr", "--lang", lang)
			render := func() (fs.FS, error) {
				out, err := generator(t).Run(t.Context(), in)
				if err != nil {
					return nil, err
				}
				return out.Plan.FS(), nil
			}
			got, err := render()
			if err != nil {
				t.Fatal(err)
			}
			testutil.Golden(t, "service-"+lang, got)
			testutil.Deterministic(t, render)
		})
	}
}

func TestPublicService(t *testing.T) {
	t.Parallel()
	in := input(t, config, "ocr", "--lang", "node", "--public", "/svc/ocr", "--auth", "none")
	out, err := generator(t).Run(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(out.Plan.FS(), "spinup.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"health = \"/healthz\"\npublic = \"/svc/ocr\"\nauth = \"none\"\nopenapi",
		"healthcheck = [\"/usr/local/bin/node\", \"/app/src/healthcheck.ts\"]\n",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("spinup.toml does not contain %q:\n%s", want, data)
		}
	}
}

func TestPublicServiceSessionAuth(t *testing.T) {
	t.Parallel()
	withAuth := "modules = [\"auth\"]\n\n" + config
	in := input(t, withAuth, "ocr", "--lang", "node", "--public", "/svc/ocr", "--auth", "session")
	if _, err := generator(t).Run(t.Context(), in); err != nil {
		t.Fatal(err)
	}
}
