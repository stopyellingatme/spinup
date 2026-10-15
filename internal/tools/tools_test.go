package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/stackversions"
)

func TestInstall(t *testing.T) {
	goodSOPS := []byte("#!/bin/sh\necho sops 3.13.3\n")
	goodAge := tarGzip(t, map[string]string{"age/age": "#!/bin/sh\necho 1.3.2\n", "age/age-keygen": "#!/bin/sh\necho 1.3.2\n"})
	hostileAge := tarGzip(t, map[string]string{"../escape": "no"})
	client := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		var body []byte
		switch filepath.Base(r.URL.Path) {
		case "sops":
			body = goodSOPS
		case "age":
			body = goodAge
		case "hostile":
			body = hostileAge
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("missing")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}

	withCatalog(t, catalog(goodSOPS, goodAge))
	baseURL := func(_, _, asset string) string { return "https://tools.test/" + asset }

	t.Run("installs verified binaries", func(t *testing.T) {
		dir := t.TempDir()
		got, err := Install(context.Background(), Options{Dir: dir, GOOS: "linux", GOARCH: "amd64", BaseURL: baseURL, HTTPClient: client})
		if err != nil || len(got) != 2 {
			t.Fatalf("Install() = %#v, %v", got, err)
		}
		for _, name := range []string{"sops", "age", "age-keygen"} {
			info, statErr := os.Stat(filepath.Join(dir, name))
			if statErr != nil || info.Mode().Perm() != 0755 {
				t.Fatalf("%s = %v, %v", name, info, statErr)
			}
		}
	})

	t.Run("removes bad checksum", func(t *testing.T) {
		dir := t.TempDir()
		bad := catalog(goodSOPS, goodAge)
		binary := bad["sops"]
		asset := binary.Assets["linux-amd64"]
		asset.SHA256 = strings.Repeat("0", 64)
		binary.Assets["linux-amd64"] = asset
		bad["sops"] = binary
		withCatalog(t, bad)
		_, err := Install(context.Background(), Options{Names: []string{"sops"}, Dir: dir, GOOS: "linux", GOARCH: "amd64", BaseURL: baseURL, HTTPClient: client})
		if err == nil || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("Install() error = %v", err)
		}
		if entries, readErr := os.ReadDir(dir); readErr != nil || len(entries) != 0 {
			t.Fatalf("target entries = %v, %v", entries, readErr)
		}
	})

	t.Run("rejects hostile archive", func(t *testing.T) {
		dir := t.TempDir()
		bad := catalog(goodSOPS, hostileAge)
		binary := bad["age"]
		asset := binary.Assets["linux-amd64"]
		asset.Name = "hostile"
		binary.Assets["linux-amd64"] = asset
		bad["age"] = binary
		withCatalog(t, bad)
		_, err := Install(context.Background(), Options{Names: []string{"age"}, Dir: dir, GOOS: "linux", GOARCH: "amd64", BaseURL: baseURL, HTTPClient: client})
		if err == nil || !strings.Contains(err.Error(), "archive") {
			t.Fatalf("Install() error = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "age")); !os.IsNotExist(statErr) {
			t.Fatalf("age exists after rejected archive: %v", statErr)
		}
	})

	t.Run("reports supported platforms", func(t *testing.T) {
		_, err := Install(context.Background(), Options{Names: []string{"sops"}, Dir: t.TempDir(), GOOS: "plan9", GOARCH: "amd64", BaseURL: baseURL, HTTPClient: client})
		if err == nil || !strings.Contains(err.Error(), "linux-amd64") {
			t.Fatalf("Install() error = %v", err)
		}
	})

	t.Run("skips current version unless forced", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sops")
		if err := os.WriteFile(path, goodSOPS, 0755); err != nil {
			t.Fatal(err)
		}
		got, err := Install(context.Background(), Options{Names: []string{"sops"}, Dir: dir, GOOS: "linux", GOARCH: "amd64", BaseURL: baseURL, HTTPClient: client})
		if err != nil || len(got) != 1 || !got[0].Skipped {
			t.Fatalf("Install() = %#v, %v", got, err)
		}
		got, err = Install(context.Background(), Options{Names: []string{"sops"}, Dir: dir, GOOS: "linux", GOARCH: "amd64", BaseURL: baseURL, HTTPClient: client, Force: true})
		if err != nil || len(got) != 1 || got[0].Skipped {
			t.Fatalf("forced Install() = %#v, %v", got, err)
		}
	})
}

func catalog(sops, age []byte) map[string]stackversions.Binary {
	return map[string]stackversions.Binary{
		"sops": {Version: "3.13.3", Repo: "test/sops", Assets: map[string]stackversions.Asset{"linux-amd64": {Name: "sops", SHA256: digest(sops)}}},
		"age":  {Version: "1.3.2", Repo: "test/age", Assets: map[string]stackversions.Asset{"linux-amd64": {Name: "age", SHA256: digest(age)}}},
	}
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func withCatalog(t *testing.T, binaries map[string]stackversions.Binary) {
	t.Helper()
	previous := loadBinaries
	loadBinaries = func() (map[string]stackversions.Binary, error) { return binaries, nil }
	t.Cleanup(func() { loadBinaries = previous })
}

type roundTripper func(*http.Request) (*http.Response, error)

func (r roundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return r(request) }

func tarGzip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var result bytes.Buffer
	gz := gzip.NewWriter(&result)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}
