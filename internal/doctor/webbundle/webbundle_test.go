package webbundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
)

func TestCheck(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		files   map[string]string
		budget  string
		status  doctor.Status
		message string
		try     bool
	}{
		{name: "node build", files: map[string]string{"web/build/client/_app/immutable/a.js": "1234", "web/build/client/_app/immutable/a.js.br": "12", "web/build/client/_app/immutable/site.css": "123", "web/build/client/_app/immutable/site.css.br": "1"}, status: doctor.OK, message: "7 bytes raw, 3 bytes brotli"},
		{name: "embed build", files: map[string]string{"web/build/_app/immutable/a.js": "1234", "web/build/_app/immutable/a.js.br": "12"}, status: doctor.OK, message: "4 bytes raw, 2 bytes brotli"},
		{name: "raw limit", files: map[string]string{"web/build/client/_app/immutable/a.js": strings.Repeat("a", 1234), "web/build/client/_app/immutable/b.css": strings.Repeat("b", 12), "web/build/client/_app/immutable/c.js": strings.Repeat("c", 123)}, budget: `{"client_raw_bytes": 10}`, status: doctor.Warn, message: "1,369 bytes raw (limit 10 bytes)", try: true},
		{name: "brotli limit", files: map[string]string{"web/build/client/_app/immutable/a.js": "1234", "web/build/client/_app/immutable/a.js.br": strings.Repeat("a", 123)}, budget: `{"client_br_bytes": 10}`, status: doctor.Warn, message: "123 bytes brotli (limit 10 bytes)", try: true},
		{name: "malformed budget", files: map[string]string{"web/build/client/_app/immutable/a.js": "1234"}, budget: `{`, status: doctor.Fail, message: "web/bundle-budget.json", try: true},
		{name: "negative budget", files: map[string]string{"web/build/client/_app/immutable/a.js": "1234"}, budget: `{"client_raw_bytes": -1}`, status: doctor.Fail, message: "web/bundle-budget.json", try: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for path, data := range test.files {
				write(t, root, path, data)
			}
			if test.budget != "" {
				write(t, root, "web/bundle-budget.json", test.budget)
			}
			got := runCheck(context.Background(), &doctor.Context{Root: root})
			if got.Status != test.status || !strings.Contains(got.Message, test.message) {
				t.Fatalf("result = %+v, want status %s and message containing %q", got, test.status, test.message)
			}
			if (got.Try != "") != test.try {
				t.Fatalf("result Try = %q, want present %t", got.Try, test.try)
			}
		})
	}
}

func TestCheckSkipsWithoutWebOrBuild(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if got := runCheck(context.Background(), &doctor.Context{Root: root}); got.Status != doctor.Skip || !strings.Contains(got.Message, "no web directory") {
		t.Fatalf("without web = %+v", got)
	}
	if err := os.Mkdir(filepath.Join(root, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(context.Background(), &doctor.Context{Root: root}); got.Status != doctor.Skip || !strings.Contains(got.Message, "build the web app first") {
		t.Fatalf("without build = %+v", got)
	}
}

func TestRegistration(t *testing.T) {
	t.Parallel()
	for _, check := range doctor.Checks(doctor.Local, false) {
		if check.ID != "web-bundle" {
			continue
		}
		if check.Quick {
			t.Fatal("web-bundle is quick")
		}
		if got := check.Run(context.Background(), &doctor.Context{}); got.Status != doctor.Skip {
			t.Fatalf("outside project = %+v", got)
		}
		return
	}
	t.Fatal("web-bundle is not registered")
}

func write(t *testing.T, root, path, data string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
