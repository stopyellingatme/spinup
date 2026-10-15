package drift

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"

	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestConflictMarkers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		data string
		want bool
	}{
		{"<<<<<<< ours\nours\n=======\ntheirs\n>>>>>>> theirs\n", true},
		{"<<<<<<< ours\nours\n||||||| base\nbase\n=======\ntheirs\n>>>>>>> theirs\n", true},
		{"<<<<<<< ours\n", false}, {"=======\n", false}, {">>>>>>> theirs\n", false},
		{"Title\n=======\n", false}, {" =======\n", false}, {"========\n", false}, {"normal\n", false},
	} {
		t.Run(tc.data, func(t *testing.T) {
			t.Parallel()
			if got := conflictMarkers([]byte(tc.data)); got != tc.want {
				t.Fatalf("markers=%v, want %v", got, tc.want)
			}
		})
	}
}

type testSource struct {
	version *templates.Version
	err     error
}

func (s testSource) Load(context.Context, string) (*templates.Version, error) {
	return s.version, s.err
}
func (s testSource) Versions(context.Context) ([]string, error) {
	return []string{"0.1.0", "0.2.0"}, nil
}

func fixture(t *testing.T) (Options, *manifest.Lock) {
	t.Helper()
	root := t.TempDir()
	packs := fstest.MapFS{
		"base/pack.toml": {Data: []byte("name = 'base'\n[[files]]\npath = 'file.txt'\nsource = 'file.txt'\nclass = 'managed'\n")},
		"base/file.txt":  {Data: []byte("template\n")},
	}
	lock := manifest.New()
	lock.TemplateVersion = "0.1.0"
	lock.Packs = []string{"base"}
	lock.Files["file.txt"] = manifest.FileEntry{Class: manifest.Managed, Template: "base/file.txt", Hash: manifest.Hash([]byte("template\n")), Mode: "0644"}
	for name, data := range map[string][]byte{"spinup.toml": nil, "file.txt": []byte("template\n")} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	return Options{Dir: root, Source: testSource{version: &templates.Version{Version: "0.1.0", Packs: packs, Stack: stackversions.MustLoad()}}}, lock
}

func TestBehindAndUnavailable(t *testing.T) {
	t.Parallel()
	o, _ := fixture(t)
	var output bytes.Buffer
	p := ui.New(nil, &output, &output, ui.Options{})
	if err := Check(context.Background(), o, false, p); ui.ExitCode(err) != 4 || !strings.Contains(output.String(), "0.2.0") {
		t.Fatalf("check=%v, output=%s", err, &output)
	}
	if err := Check(context.Background(), o, true, p); err != nil {
		t.Fatal(err)
	}
	o.Source = testSource{err: fmt.Errorf("%w\nTry: spinup cache warm", templates.ErrVersionUnavailable)}
	if err := Diff(context.Background(), o, nil, p); !errors.Is(err, templates.ErrVersionUnavailable) || !strings.HasSuffix(err.Error(), "Try: spinup cache warm") {
		t.Fatalf("error=%v", err)
	}
}

func TestCheckReportsUnavailableTemplateWithDrift(t *testing.T) {
	t.Parallel()
	o, _ := fixture(t)
	if err := os.WriteFile(filepath.Join(o.Dir, "file.txt"), []byte("edited\n"), 0644); err != nil {
		t.Fatal(err)
	}
	o.JSON = true
	o.Source = testSource{err: fmt.Errorf("%w\nTry: spinup cache warm", templates.ErrVersionUnavailable)}
	var output bytes.Buffer
	err := Check(context.Background(), o, false, ui.New(nil, &output, &output, ui.Options{}))
	if ui.ExitCode(err) != ui.ExitDrift {
		t.Fatalf("check=%v, output=%s", err, &output)
	}
	if !json.Valid(output.Bytes()) || !strings.Contains(output.String(), `"state": "template-unavailable"`) || !strings.Contains(output.String(), `"state": "drift"`) {
		t.Fatalf("output=%s", &output)
	}
	output.Reset()
	err = Check(context.Background(), o, true, ui.New(nil, &output, &output, ui.Options{}))
	if ui.ExitCode(err) != ui.ExitDrift || !json.Valid(output.Bytes()) || strings.Contains(output.String(), "template-unavailable") || !strings.Contains(output.String(), `"state": "drift"`) {
		t.Fatalf("allow-behind check=%v, output=%s", err, &output)
	}
}

func TestCheckReportsNonRegularPathWithOtherDrift(t *testing.T) {
	t.Parallel()
	o, lock := fixture(t)
	lock.Files["other.txt"] = manifest.FileEntry{Class: manifest.Managed, Template: "base/other.txt", Hash: manifest.Hash([]byte("original\n")), Mode: "0644"}
	if err := lock.Save(filepath.Join(o.Dir, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.Dir, "other.txt"), []byte("edited\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(o.Dir, "file.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(o.Dir, "other.txt"), filepath.Join(o.Dir, "file.txt")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := Check(context.Background(), o, true, ui.New(nil, &output, &output, ui.Options{}))
	if ui.ExitCode(err) != ui.ExitDrift || !strings.Contains(output.String(), "not-regular") || !strings.Contains(output.String(), "managed file differs") {
		t.Fatalf("check=%v, output=%s", err, &output)
	}
}

func TestDiffStatesAndSafeRestore(t *testing.T) {
	t.Parallel()
	o, lock := fixture(t)
	var output bytes.Buffer
	p := ui.New(nil, &output, &output, ui.Options{})
	lock.Tombstone("file.txt")
	if err := lock.Save(filepath.Join(o.Dir, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	if err := Diff(context.Background(), o, nil, p); err != nil || !strings.Contains(output.String(), "tombstone") {
		t.Fatalf("diff=%v: %s", err, &output)
	}
	if err := Restore(context.Background(), o, "file.txt", false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(o.Dir, "file.txt")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Check(context.Background(), o, true, p); ui.ExitCode(err) != 4 || !strings.Contains(output.String(), "managed file is missing") {
		t.Fatalf("missing check=%v: %s", err, &output)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(o.Dir, "file.txt")); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), o, "file.txt", true); err == nil {
		t.Fatal("restored through symlink")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatalf("outside=%q %v", data, err)
	}
	if err := Restore(context.Background(), o, "../outside", true); err == nil {
		t.Fatal("accepted parent path")
	}
}

func TestDoctorOutput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		version string
		code    int
	}{{"2.44.0", 0}, {"2.43.0", 1}} {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()
			fake := &exec.Fake{}
			fake.Expect("git", "--version").Return("git version "+tc.version+"\n", "", 0)
			var output bytes.Buffer
			err := Doctor(context.Background(), Options{Dir: t.TempDir(), Version: "0.0.0-dev", JSON: true}, true, fake, ui.New(nil, &output, &output, ui.Options{}))
			if ui.ExitCode(err) != tc.code || !json.Valid(output.Bytes()) {
				t.Fatalf("doctor=%v: %s", err, &output)
			}
			fake.AssertDone(t)
		})
	}
}
