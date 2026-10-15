package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"

	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCheckChannels(t *testing.T) {
	t.Parallel()
	srv := newTestHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/tags/") {
			_, _ = w.Write([]byte(`{"tag_name":"v0.9.0","prerelease":true}`))
			return
		}
		_, _ = w.Write([]byte(`[
 {"tag_name":"v2.0.0","draft":true,"published_at":"2026-10-15T00:00:00Z"},
 {"tag_name":"v1.3.0-rc.1","prerelease":true,"published_at":"2026-10-14T00:00:00Z"},
 {"tag_name":"v1.1.0","published_at":"2026-10-12T00:00:00Z"},
 {"tag_name":"v1.2.0","published_at":"2026-10-13T00:00:00Z"}]`))
	}))
	defer srv.Close()
	for _, tt := range []struct {
		name, channel, version, want string
		available                    bool
	}{
		{"stable", "stable", "", "1.2.0", true}, {"next", "next", "", "1.3.0-rc.1", true}, {"explicit", "stable", "0.9.0", "0.9.0", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, err := (Updater{APIBase: srv.URL, Client: srv.Client()}).Check(context.Background(), Options{Current: "1.1.0", Channel: tt.channel, Version: tt.version})
			if err != nil || r.Version != tt.want || r.Available != tt.available {
				t.Fatalf("result=%+v error=%v", r, err)
			}
		})
	}
	for _, o := range []Options{{Channel: "invalid"}, {Version: "../../bad"}} {
		if _, err := (Updater{APIBase: srv.URL, Client: srv.Client()}).Check(context.Background(), o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
}

func TestCheckChoosesHighestVersionBeforePublishTime(t *testing.T) {
	t.Parallel()
	srv := newTestHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
 {"tag_name":"v1.3.0","published_at":"2026-10-12T00:00:00Z"},
 {"tag_name":"v1.2.5","published_at":"2026-10-15T00:00:00Z"}]`))
	}))
	defer srv.Close()

	r, err := (Updater{APIBase: srv.URL, Client: srv.Client()}).Check(context.Background(), Options{Current: "1.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "1.3.0" {
		t.Fatalf("version = %q, want 1.3.0", r.Version)
	}
}

func TestCheckDoesNotReplace(t *testing.T) {
	t.Parallel()
	srv := fixture(t, false, false)
	path := filepath.Join(t.TempDir(), "spinup")
	if err := os.WriteFile(path, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	r, err := (Updater{APIBase: srv.URL, Client: srv.Client(), Executable: path}).Update(context.Background(), Options{Current: "1.0.0", Check: true})
	if err != nil || !r.Available || r.Updated {
		t.Fatalf("result=%+v error=%v", r, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "old" {
		t.Fatal("check changed the binary")
	}
}

func TestCheckWorksForGuardedInstalls(t *testing.T) {
	t.Parallel()
	srv := fixture(t, false, false)
	for _, tt := range []struct {
		name, current, executable string
	}{
		{name: "Homebrew", current: "1.0.0", executable: "/opt/homebrew/bin/spinup"},
		{name: "development", current: "dev", executable: filepath.Join(t.TempDir(), "spinup")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, err := (Updater{APIBase: srv.URL, Client: srv.Client(), Executable: tt.executable}).Update(context.Background(), Options{Current: tt.current, Check: true})
			if err != nil {
				t.Fatal(err)
			}
			if r.Version != "1.2.0" || !r.Available || r.Command != "" || r.Updated {
				t.Fatalf("result = %+v", r)
			}
		})
	}
}

func TestReplace(t *testing.T) {
	t.Parallel()
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "spinup")
			if err := os.WriteFile(path, []byte("old"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := replace(path, []byte("new"), goos); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			info, _ := os.Stat(path)
			if string(data) != "new" || (runtime.GOOS != "windows" && info.Mode().Perm() != 0755) {
				t.Fatalf("binary=%s mode=%v", data, info.Mode())
			}
			if goos == "windows" {
				old, _ := os.ReadFile(path + ".old")
				if string(old) != "old" {
					t.Fatal("old binary missing")
				}
			}
			leftovers, _ := filepath.Glob(filepath.Join(dir, ".spinup-*"))
			if len(leftovers) != 0 {
				t.Fatal(leftovers)
			}
		})
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(dir, 0755); err != nil {
			t.Error(err)
		}
	}()
	if err := replace(filepath.Join(dir, "spinup"), []byte("new"), "linux"); err == nil || !strings.Contains(err.Error(), "re-run with permissions or reinstall") {
		t.Fatalf("error=%v", err)
	}
}

func TestExtract(t *testing.T) {
	t.Parallel()
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	f, err := zw.Create("spinup.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("exe")); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if b, e := extract(zipped.Bytes(), "windows"); e != nil || string(b) != "exe" {
		t.Fatalf("binary=%q error=%v", b, e)
	}
	for _, tt := range []struct {
		name    string
		headers []*tar.Header
	}{
		{"missing", []*tar.Header{{Name: "../spinup", Typeflag: tar.TypeReg}}},
		{"symlink", []*tar.Header{{Name: "spinup", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}},
		{"duplicate", []*tar.Header{{Name: "spinup", Typeflag: tar.TypeReg, Size: 1}, {Name: "spinup", Typeflag: tar.TypeReg, Size: 1}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			gz := gzip.NewWriter(&b)
			tw := tar.NewWriter(gz)
			for _, h := range tt.headers {
				if e := tw.WriteHeader(h); e != nil {
					t.Fatal(e)
				}
				if h.Size > 0 {
					if _, e := tw.Write([]byte("x")); e != nil {
						t.Fatal(e)
					}
				}
			}
			if e := tw.Close(); e != nil {
				t.Fatal(e)
			}
			if e := gz.Close(); e != nil {
				t.Fatal(e)
			}
			if _, e := extract(b.Bytes(), "linux"); e == nil {
				t.Fatal("accepted invalid archive")
			}
		})
	}
}

func TestHomebrew(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		path string
		want bool
	}{
		{"/opt/homebrew/bin/spinup", true}, {"/usr/local/Cellar/spinup/1/spinup", true}, {"/custom/Caskroom/spinup/1/spinup", true}, {"/home/linuxbrew/.linuxbrew/bin/spinup", true}, {"/home/user/bin/spinup", false}, {"/opt/homebrew-other/bin/spinup", false},
	} {
		if got := Homebrew(tt.path); got != tt.want {
			t.Errorf("Homebrew(%s)=%v", tt.path, got)
		}
	}
}
