package serviceclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func TestPostStep(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		services   string
		openAPI    bool
		scaffold   string
		wantError  string
		wantCalls  int
		wantConfig string
	}{
		{
			name: "generate",
			services: `
[[services]]
name = "ocr"
port = 8080
dev_port = 8101
health = "/healthz"
openapi = "openapi.yaml"
`,
			openAPI:    true,
			wantCalls:  1,
			wantConfig: "package: ocr\noutput: client.gen.go\ngenerate:\n  client: true\n  models: true\n",
		},
		{
			name: "preserve scaffold",
			services: `
[[services]]
name = "ocr"
port = 8080
dev_port = 8101
health = "/healthz"
openapi = "openapi.yaml"
`,
			openAPI:   true,
			scaffold:  "// user scaffold\n",
			wantCalls: 1,
		},
		{
			name: "missing OpenAPI",
			services: `
[[services]]
name = "ocr"
port = 8080
dev_port = 8101
health = "/healthz"
openapi = "openapi.yaml"
`,
			wantError: "does not exist",
		},
		{name: "no services", wantCalls: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			write(t, root, "spinup.toml", baseConfig+tt.services)
			if tt.openAPI {
				write(t, root, "services/ocr/openapi.yaml", "openapi: 3.0.3\ninfo: {title: OCR, version: 1.0.0}\npaths: {}\n")
			}
			if tt.scaffold != "" {
				write(t, root, "api/internal/services/ocr/ocr.go", tt.scaffold)
			}
			if tt.wantCalls > 0 {
				write(t, root, "api/client.gen.go", "package ocr\n")
			}
			fake := &exec.Fake{}
			if tt.wantCalls > 0 {
				fake.Expect("go", "tool", "-modfile=tools.mod", "oapi-codegen", "-config", "internal/services/ocr/oapi-codegen.yaml", "../services/ocr/openapi.yaml")
			}
			err := postStep(context.Background(), root, nil, fake)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			fake.AssertDone(t)
			if len(fake.Calls()) != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", len(fake.Calls()), tt.wantCalls)
			}
			if tt.wantCalls == 0 {
				return
			}
			config, err := os.ReadFile(filepath.Join(root, "api/internal/services/ocr/oapi-codegen.yaml"))
			if err != nil || string(config) != tt.wantConfig && tt.wantConfig != "" {
				t.Fatalf("configuration = %q, %v", config, err)
			}
			scaffold, err := os.ReadFile(filepath.Join(root, "api/internal/services/ocr/ocr.go"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "api/client.gen.go")); !os.IsNotExist(err) {
				t.Fatalf("temporary generated client remains: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "api/internal/services/ocr/client.gen.go")); err != nil {
				t.Fatal(err)
			}
			if tt.scaffold != "" {
				if string(scaffold) != tt.scaffold {
					t.Fatalf("scaffold = %q, want preserved %q", scaffold, tt.scaffold)
				}
				return
			}
			for _, want := range []string{"env:\"SERVICE_OCR_URL\" required:\"true\"", "otelhttp.NewTransport(http.DefaultTransport)", "Timeout:   30 * time.Second", "NewClientWithResponses(cfg.URL, WithHTTPClient(client))"} {
				if !strings.Contains(string(scaffold), want) {
					t.Fatalf("scaffold missing %q:\n%s", want, scaffold)
				}
			}
		})
	}
}

const baseConfig = `[app]
name = "todo"
module = "example.com/todo"
`

func write(t *testing.T, root, path, data string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}
