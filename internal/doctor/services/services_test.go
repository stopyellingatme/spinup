package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
)

func TestFindings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		config  string
		files   map[string]string
		status  doctor.Status
		want    string
		wantTry string
	}{
		{name: "no services", config: "[app]\nname='demo'\nmodule='example.com/demo'\n", status: doctor.OK, want: "no sidecar services"},
		{name: "invalid service", config: serviceConfig("BAD", "openapi.yaml", ""), status: doctor.Fail, want: "invalid services", wantTry: "correct the [[services]]"},
		{name: "missing Dockerfile", config: serviceConfig("ocr", "", ""), status: doctor.Fail, want: "services/ocr/Dockerfile", wantTry: "add a Dockerfile"},
		{name: "missing OpenAPI", config: serviceConfig("ocr", "openapi.yaml", ""), files: map[string]string{"services/ocr/Dockerfile": "FROM scratch\n"}, status: doctor.Fail, want: "services/ocr/openapi.yaml", wantTry: "add the OpenAPI file"},
		{name: "missing client", config: serviceConfig("ocr", "openapi.yaml", ""), files: map[string]string{"services/ocr/Dockerfile": "FROM scratch\n", "services/ocr/openapi.yaml": "openapi: 3.0.0\n"}, status: doctor.Warn, want: "generated Go client is missing", wantTry: "spinup g client"},
		{name: "missing secret", config: serviceConfig("ocr", "", "OCR_LICENSE_KEY"), files: map[string]string{"services/ocr/Dockerfile": "FROM scratch\n", "secrets/prod.enc.yaml": "sops: {}\nOTHER: ENC[value]\n"}, status: doctor.Warn, want: "OCR_LICENSE_KEY", wantTry: "add OCR_LICENSE_KEY"},
		{name: "secret exists across files", config: serviceConfig("ocr", "", "OCR_LICENSE_KEY"), files: map[string]string{"services/ocr/Dockerfile": "FROM scratch\n", "secrets/prod.enc.yaml": "sops: {}\nOCR_LICENSE_KEY: ENC[never print this]\n"}, status: doctor.OK, want: "declared sidecar services"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, "spinup.toml", tc.config)
			for name, data := range tc.files {
				writeFile(t, root, name, data)
			}
			got := runCheck(context.Background(), &doctor.Context{Root: root})
			if got.Status != tc.status || !strings.Contains(got.Message, tc.want) {
				t.Fatalf("result = %+v, want status %s containing %q", got, tc.status, tc.want)
			}
			if tc.wantTry != "" && !strings.Contains(got.Message, "Try: "+tc.wantTry) {
				t.Fatalf("result has no Try line %q: %+v", tc.wantTry, got)
			}
			if strings.Contains(got.Message, "never print this") {
				t.Fatal("secret value was printed")
			}
		})
	}
}

func TestRegistration(t *testing.T) {
	t.Parallel()
	found := false
	for _, check := range doctor.Checks(doctor.Local, false) {
		if check.ID == "services" {
			found = true
			if got := check.Run(context.Background(), &doctor.Context{}); got.Status != doctor.Skip {
				t.Fatalf("outside project: %+v", got)
			}
		}
	}
	if !found {
		t.Fatal("services check is not registered")
	}
}

func serviceConfig(name, openapi, secret string) string {
	config := "[app]\nname='demo'\nmodule='example.com/demo'\n\n[[services]]\nname='" + name + "'\nport=8080\ndev_port=8101\nhealth='/healthz'\n"
	if openapi != "" {
		config += "openapi='" + openapi + "'\n"
	}
	if secret != "" {
		config += "secrets=['" + secret + "']\n"
	}
	return config
}

func writeFile(t *testing.T, root, name, data string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
