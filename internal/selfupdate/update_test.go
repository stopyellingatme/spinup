package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"

	"os"
	"path/filepath"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

type fakeRunner struct {
	fail   bool
	called bool
}

func (r *fakeRunner) Run(_ context.Context, c runexec.Cmd) (runexec.Result, error) {
	r.called = true
	if c.Name != "fake-cosign" || !strings.Contains(strings.Join(c.Args, " "), "release.yml@refs/tags/v1.2.0") {
		return runexec.Result{}, fmt.Errorf("wrong signature identity: %v", c)
	}
	if r.fail {
		return runexec.Result{}, fmt.Errorf("invalid signature")
	}
	return runexec.Result{}, nil
}

func fixture(t *testing.T, bad, missing bool) *testHTTP {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "spinup", Mode: 0755, Size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := b.Bytes()
	sum := fmt.Sprintf("%x", sha256.Sum256(archive))
	if bad {
		sum = strings.Repeat("0", 64)
	}
	asset := "spinup_1.2.0_linux_amd64.tar.gz"
	srv := newTestHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases", "/releases/tags/v1.2.0":
			assets := []assetInfo{{Name: asset}, {Name: "checksums.txt"}, {Name: "checksums.txt.sigstore.json"}}
			if missing {
				assets = assets[1:]
			}
			rel := release{Tag: "v1.2.0", Assets: assets}
			if strings.Contains(r.URL.Path, "/tags/") {
				_ = json.NewEncoder(w).Encode(rel)
			} else {
				_ = json.NewEncoder(w).Encode([]release{rel})
			}
		case "/download/v1.2.0/" + asset:
			_, _ = w.Write(archive)
		case "/download/v1.2.0/checksums.txt":
			_, _ = fmt.Fprintf(w, "%s  %s\n", sum, asset)
		case "/download/v1.2.0/checksums.txt.sigstore.json":
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                                string
		bad, missing, cosign, fail, require bool
		want                                string
	}{
		{name: "good"}, {name: "checksum", bad: true, want: "checksum"}, {name: "missing", missing: true, want: "asset"},
		{name: "signature", cosign: true}, {name: "signature failure", cosign: true, fail: true, want: "signature"},
		{name: "required", require: true, want: "cosign"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := fixture(t, tt.bad, tt.missing)
			path := filepath.Join(t.TempDir(), "spinup")
			if err := runexec.WriteExecutable(path, []byte("old"), 0755); err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{fail: tt.fail}
			u := Updater{APIBase: srv.URL, Client: srv.Client(), DownloadBase: srv.URL + "/download", OS: "linux", Arch: "amd64", Executable: path, Runner: runner, LookPath: func(string) (string, error) {
				if tt.cosign {
					return "fake-cosign", nil
				}
				return "", fmt.Errorf("missing")
			}}
			result, err := u.Update(context.Background(), Options{Current: "1.0.0", RequireSignature: tt.require})
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %v, want %s", err, tt.want)
				}
			} else if err != nil || !result.Updated {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			data, _ := os.ReadFile(path)
			want := "new"
			if tt.want != "" {
				want = "old"
			}
			if string(data) != want {
				t.Fatalf("binary=%q", data)
			}
			if tt.cosign && !runner.called {
				t.Fatal("signature verification did not run")
			}
		})
	}
}

func TestGuards(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, current, path, version, want string }{
		{"dev", "dev", "/unused", "", "development"}, {"alpha", "1.0.0-alpha.123", "/unused", "", "development"},
		{"brew", "1.0.0", "/opt/homebrew/Caskroom/spinup/1/bin/spinup", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u := Updater{Executable: tt.path}
			r, err := u.Update(context.Background(), Options{Current: tt.current, Version: tt.version})
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil || r.Command != "brew upgrade --cask spinup" {
				t.Fatalf("result=%+v error=%v", r, err)
			}
		})
	}
}

func TestExplicitDevelopmentUpdate(t *testing.T) {
	t.Parallel()
	for _, current := range []string{"dev", "1.2.0-alpha.12"} {
		t.Run(current, func(t *testing.T) {
			t.Parallel()
			srv := fixture(t, false, false)
			path := filepath.Join(t.TempDir(), "spinup")
			if err := runexec.WriteExecutable(path, []byte("old"), 0755); err != nil {
				t.Fatal(err)
			}
			u := Updater{APIBase: srv.URL, DownloadBase: srv.URL + "/download", Client: srv.Client(), Executable: path, OS: "linux", Arch: "amd64", LookPath: func(string) (string, error) { return "", fmt.Errorf("absent") }}
			r, err := u.Update(context.Background(), Options{Current: current, Version: "1.2.0"})
			if err != nil || !r.Updated || r.Warning == "" {
				t.Fatalf("result=%+v error=%v", r, err)
			}
		})
	}
}

func TestHomebrewSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "Caskroom", "spinup", "spinup")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := runexec.WriteExecutable(target, []byte("brew"), 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "spinup")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	r, err := (Updater{Executable: link}).Update(context.Background(), Options{Current: "1.0.0"})
	if err != nil || r.Command != "brew upgrade --cask spinup" {
		t.Fatalf("result=%+v error=%v", r, err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "brew" {
		t.Fatal("Homebrew binary changed")
	}
}
