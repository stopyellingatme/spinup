package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/stackversions"
)

func tofuZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0755)
		if strings.HasPrefix(name, "symlink:") {
			header.Name = strings.TrimPrefix(name, "symlink:")
			header.SetMode(os.ModeSymlink | 0755)
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("binary")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallTofu(t *testing.T) {
	for _, tc := range []struct {
		name        string
		files       []string
		badChecksum bool
		want        string
	}{
		{name: "valid", files: []string{"tofu", "LICENSE", "README.md", "CHANGELOG.md"}},
		{name: "checksum", files: []string{"tofu"}, badChecksum: true, want: "checksum"},
		{name: "traversal", files: []string{"../tofu"}, want: "archive"},
		{name: "absolute", files: []string{"/tofu"}, want: "archive"},
		{name: "symlink", files: []string{"symlink:tofu"}, want: "archive"},
		{name: "duplicate", files: []string{"tofu", "tofu"}, want: "archive"},
		{name: "missing", files: []string{"LICENSE"}, want: "archive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tofuZip(t, tc.files...)
			checksum := digest(body)
			if tc.badChecksum {
				checksum = strings.Repeat("0", 64)
			}
			withCatalog(t, map[string]stackversions.Binary{"tofu": {Version: "1.13.1", Repo: "opentofu/opentofu", Assets: map[string]stackversions.Asset{"linux-amd64": {Name: "tofu.zip", SHA256: checksum}}}})
			client := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
			})}
			dir := t.TempDir()
			got, err := Install(context.Background(), Options{Names: []string{"tofu"}, Dir: dir, GOOS: "linux", GOARCH: "amd64", HTTPClient: client})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("Install = %v, %v", got, err)
				}
				entries, readErr := os.ReadDir(dir)
				if readErr != nil || len(entries) != 0 {
					t.Fatalf("rejected archive left files: %v, %v", entries, readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "tofu"))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "binary" {
				t.Fatalf("installed bytes = %q", data)
			}
			info, err := os.Stat(filepath.Join(dir, "tofu"))
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0755 {
				t.Errorf("mode = %o", info.Mode().Perm())
			}
		})
	}
}

func TestDefaultToolsIncludeTofu(t *testing.T) {
	t.Parallel()
	stack := stackversions.MustLoad()
	names, err := selectedNames(nil, stack.Binaries)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "sops,age,tofu" {
		t.Fatalf("default tools = %v", names)
	}
}
