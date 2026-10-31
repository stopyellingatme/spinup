package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCIServicesJSON(t *testing.T) {
	t.Parallel()
	root := ciServiceProject(t)
	writeCIServiceFile(t, filepath.Join(root, "services", "rs", "Cargo.toml"))
	writeCIServiceFile(t, filepath.Join(root, "services", "py", "pyproject.toml"))
	writeCIServiceFile(t, filepath.Join(root, "services", "py", "uv.lock"))

	code, out, errOut := run(t, "ci", "services", "--json", "--dir", root)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut)
	}
	const want = `{"include":[{"name":"rs","lang":"rust"},{"name":"py","lang":"python"}]}` + "\n"
	if out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
}

func TestCIServicesTable(t *testing.T) {
	t.Parallel()
	root := ciServiceProject(t)
	writeCIServiceFile(t, filepath.Join(root, "services", "rs", "Cargo.toml"))

	code, out, errOut := run(t, "ci", "services", "--dir", root)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut)
	}
	const want = "NAME\tLANG\nrs\trust\npy\tnone\n"
	if out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
}

func TestCITestServiceDryRun(t *testing.T) {
	t.Parallel()
	root := ciServiceProject(t)
	writeCIServiceFile(t, filepath.Join(root, "services", "rs", "Cargo.toml"))

	code, out, errOut := run(t, "ci", "test-service", "rs", "--dry-run", "--dir", root)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut)
	}
	if want := "RUN cargo test --locked\n"; !strings.Contains(out, want) {
		t.Fatalf("stdout does not contain %q: %s", want, out)
	}
}

func ciServiceProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	const config = `[app]
name = "todo"
module = "example.com/todo"

[[services]]
name = "rs"
port = 8080
dev_port = 8101
health = "/healthz"

[[services]]
name = "py"
port = 8080
dev_port = 8102
health = "/healthz"
`
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeCIServiceFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
}
