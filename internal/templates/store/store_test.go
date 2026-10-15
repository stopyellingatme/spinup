package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/templates"
)

func archiveData(t *testing.T, marker string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteArchive(&buf, fstest.MapFS{"base/marker": &fstest.MapFile{Data: []byte(marker), Mode: 0644}}, stackData(t), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testKey() (ed25519.PublicKey, ed25519.PrivateKey) {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, ed25519.SeedSize))
	return private.Public().(ed25519.PublicKey), private
}

func writeFile(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, want string }{
		{"dev", "0.0.0-dev"}, {"", "0.0.0-dev"}, {"branch-sha", "0.0.0-dev"},
		{"v1.2.3", "1.2.3"}, {"1.2.3-rc.1+build.4", "1.2.3-rc.1+build.4"},
		{"1.2.3-01", "0.0.0-dev"}, {"01.2.3", "0.0.0-dev"}, {"1.2.3+", "0.0.0-dev"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			if got := CurrentVersion(tc.input); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	current := Current("dev")
	if current.Stack == nil || current.Packs == nil || current.KitMigration != nil {
		t.Fatalf("current=%#v", current)
	}
}

func TestLoadOrderAndVersions(t *testing.T) {
	t.Parallel()
	key, private := testKey()
	cache, extra := t.TempDir(), t.TempDir()
	embedded := fstest.MapFS{}
	for _, version := range []string{"0.2.0", "0.10.0", "0.2.0-rc.10", "0.2.0-rc.2"} {
		data := archiveData(t, "embedded")
		embedded["templates-v"+version+".tar.zst"] = &fstest.MapFile{Data: data}
	}
	data := archiveData(t, "cache")
	writeFile(t, filepath.Join(cache, "templates-v0.2.0.tar.zst"), data)
	writeFile(t, filepath.Join(cache, "templates-v0.2.0.tar.zst.sig"), ed25519.Sign(private, data))
	writeFile(t, filepath.Join(extra, "0.2.0", "packs", "base", "marker"), []byte("extra"))
	writeFile(t, filepath.Join(extra, "0.2.0", "stackversions.yaml"), stackData(t))
	writeFile(t, filepath.Join(extra, "0.2.0", "MIGRATION.json"), []byte("notes"))
	writeFile(t, filepath.Join(extra, "not-a-version", "stackversions.yaml"), stackData(t))
	current, err := ReadArchive(archiveData(t, "current"), "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Current: current, Embedded: embedded, CacheDir: cache, ExtraDir: extra, Offline: true, Keys: []ed25519.PublicKey{key}}
	for _, tc := range []struct {
		source string
		mutate func(*Options)
	}{
		{"current", func(*Options) {}},
		{"extra", func(o *Options) { o.Current = nil }},
		{"embedded", func(o *Options) { o.Current = nil; o.ExtraDir = "" }},
		{"cache", func(o *Options) { o.Current = nil; o.ExtraDir = ""; o.Embedded = fstest.MapFS{} }},
	} {
		t.Run(tc.source, func(t *testing.T) {
			o := options
			tc.mutate(&o)
			s, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			v, err := s.Load(context.Background(), "0.2.0")
			if err != nil {
				t.Fatal(err)
			}
			marker, err := fs.ReadFile(v.Packs, "base/marker")
			if err != nil || string(marker) != tc.source {
				t.Fatalf("marker=%q, err=%v", marker, err)
			}
			entries, err := s.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if e.Version == "0.2.0" && e.Source != tc.source {
					t.Fatalf("entry=%#v", e)
				}
			}
		})
	}
	s, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := s.Versions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0.2.0-rc.2", "0.2.0-rc.10", "0.2.0", "0.10.0"}
	if !reflect.DeepEqual(versions, want) {
		t.Fatalf("versions=%q, want %q", versions, want)
	}
}

func TestOfflineAndInvalidVersion(t *testing.T) {
	t.Parallel()
	s, err := New(Options{CacheDir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Load(context.Background(), "1.0.0")
	if !errors.Is(err, templates.ErrVersionUnavailable) || !strings.HasSuffix(err.Error(), "Try: run once without --offline") {
		t.Fatalf("error=%v", err)
	}
	if err := s.Warm(context.Background(), []string{"1.0.0"}); err == nil {
		t.Fatal("offline warm succeeded")
	}
	for _, v := range []string{"v1.0.0", "../bad", "1.0", "1.0.0-01", "1.0.0/x", "1.0.0+../x", ""} {
		if _, err := s.Load(context.Background(), v); err == nil {
			t.Errorf("accepted %q", v)
		}
	}
}

func TestDownloadVerification(t *testing.T) {
	t.Parallel()
	key, private := testKey()
	data := archiveData(t, "download")
	for _, tc := range []struct {
		name                string
		bad, noKey, corrupt bool
	}{
		{name: "good"}, {name: "bad signature", bad: true}, {name: "no trusted key", noKey: true}, {name: "corrupt cache", corrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if !strings.HasPrefix(r.URL.Path, "/v1.2.3/templates-v1.2.3.tar.zst") {
					http.NotFound(w, r)
					return
				}
				body := data
				if strings.HasSuffix(r.URL.Path, ".sig") {
					body = ed25519.Sign(private, data)
					if tc.bad {
						body[0] ^= 1
					}
				}
				_, _ = w.Write(body)
			})
			url, client := testServer(t, handler)
			keys := []ed25519.PublicKey{key}
			if tc.noKey {
				keys = []ed25519.PublicKey{}
			}
			cache := t.TempDir()
			s, err := New(Options{CacheDir: cache, BaseURL: url, HTTPClient: client, Keys: keys})
			if err != nil {
				t.Fatal(err)
			}
			err = s.Warm(context.Background(), []string{"1.2.3"})
			if tc.bad || tc.noKey {
				if err == nil || !errors.Is(err, templates.ErrVersionUnavailable) {
					t.Fatalf("error=%v", err)
				}
				entries, readErr := os.ReadDir(cache)
				if readErr != nil || len(entries) != 0 {
					t.Fatalf("cache=%v, %v", entries, readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 2 {
				t.Fatalf("requests=%d", requests.Load())
			}
			if tc.corrupt {
				writeFile(t, filepath.Join(cache, "templates-v1.2.3.tar.zst"), []byte("corrupted"))
			}
			_, err = s.Load(context.Background(), "1.2.3")
			if tc.corrupt {
				if err == nil || !strings.Contains(err.Error(), "signature") {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 2 {
				t.Fatal("cached load used network")
			}
			if !tc.corrupt {
				if err := s.Warm(context.Background(), []string{"1.2.3"}); err != nil {
					t.Fatal(err)
				}
				if requests.Load() != 2 {
					t.Fatal("warm downloaded cached version")
				}
			}
		})
	}
}

// testServer uses a real httptest.Server when the sandbox permits loopback sockets.
// The same HTTP handler runs through an in-memory transport in restricted lanes.
func testServer(t *testing.T, handler http.Handler) (string, *http.Client) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Logf("loopback unavailable, using HTTP transport in memory: %v", err)
		return "http://templates.test", &http.Client{Transport: handlerTransport{handler: handler}}
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	t.Cleanup(server.Close)
	return server.URL, server.Client()
}

type handlerTransport struct{ handler http.Handler }

func (tr handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	tr.handler.ServeHTTP(recorder, r)
	return recorder.Result(), nil
}

func TestDefaultEnvironment(t *testing.T) {
	t.Setenv("SPINUP_OFFLINE", "1")
	extra := t.TempDir()
	t.Setenv("SPINUP_TEMPLATES_DIR", extra)
	writeFile(t, filepath.Join(extra, "0.3.0", "packs", "base", "marker"), []byte("environment"))
	writeFile(t, filepath.Join(extra, "0.3.0", "stackversions.yaml"), stackData(t))
	s, err := Default("dev", false)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Load(context.Background(), "0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	marker, err := fs.ReadFile(v.Packs, "base/marker")
	if err != nil || string(marker) != "environment" {
		t.Fatalf("marker=%q, err=%v", marker, err)
	}
	_, err = s.Load(context.Background(), "0.4.0")
	if !errors.Is(err, templates.ErrVersionUnavailable) || !strings.HasSuffix(err.Error(), "Try: run once without --offline") {
		t.Fatalf("error=%v", err)
	}
}

func TestCacheRejectsMissingOrChangedSignature(t *testing.T) {
	t.Parallel()
	key, private := testKey()
	data := archiveData(t, "cached")
	for _, tc := range []struct {
		name string
		sig  []byte
	}{
		{"missing", nil}, {"changed", make([]byte, ed25519.SignatureSize)}, {"oversized", make([]byte, ed25519.SignatureSize+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cache := t.TempDir()
			archive := filepath.Join(cache, "templates-v1.0.0.tar.zst")
			writeFile(t, archive, data)
			if tc.sig != nil {
				writeFile(t, archive+".sig", tc.sig)
			}
			var requests atomic.Int32
			client := &http.Client{Transport: handlerTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.NotFound(w, r) })}}
			s, err := New(Options{CacheDir: cache, Keys: []ed25519.PublicKey{key}, HTTPClient: client, BaseURL: "http://templates.test"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(context.Background(), "1.0.0"); err == nil {
				t.Fatal("accepted invalid cached signature")
			}
			if requests.Load() != 0 {
				t.Fatal("invalid cache triggered a download")
			}
			entries, err := s.List(context.Background())
			if err != nil || len(entries) != 0 {
				t.Fatalf("entries=%v, err=%v", entries, err)
			}
			writeFile(t, archive+".sig", ed25519.Sign(private, data))
			if _, err := s.Load(context.Background(), "1.0.0"); err != nil {
				t.Fatal(err)
			}
			writeFile(t, archive+".sig", make([]byte, ed25519.SignatureSize))
			if _, err := s.Load(context.Background(), "1.0.0"); err == nil {
				t.Fatal("signature was not verified again")
			}
		})
	}
}

func TestInvalidInputNeverDownloads(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	client := &http.Client{Transport: handlerTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.NotFound(w, r) })}}
	key, _ := testKey()
	cache := filepath.Join(t.TempDir(), "absent")
	s, err := New(Options{CacheDir: cache, Keys: []ed25519.PublicKey{key}, HTTPClient: client, BaseURL: "http://templates.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Warm(context.Background(), []string{"1.0.0", "../../escape"}); err == nil {
		t.Fatal("accepted invalid request")
	}
	if requests.Load() != 0 {
		t.Fatal("invalid input triggered a request")
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("cache exists: %v", err)
	}
	versions, err := s.Versions(context.Background())
	if err != nil || len(versions) != 0 {
		t.Fatalf("versions=%v, err=%v", versions, err)
	}
	if requests.Load() != 0 {
		t.Fatal("version list triggered a request")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Load(ctx, "1.0.0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if _, err := s.Versions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
