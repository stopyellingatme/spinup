package build

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func embedProject(t *testing.T, manager string) string {
	t.Helper()
	root := imageProject(t, "module github.com/acme/todo/api\n")
	writeBuildFile(t, root, "spinup.toml", "[app]\nname = 'todo'\n")
	lock := manifest.New()
	lock.Answers["frontend_mode"] = "embed"
	lock.Answers["package_manager"] = manager
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeBuildFile(t *testing.T, root, name, data string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestEmbedImages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		push, explicit bool
	}{
		{name: "default load"}, {name: "default push", push: true}, {name: "explicit api", explicit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := embedProject(t, "pnpm")
			runner := &metadataRunner{digest: "sha256:api"}
			o := ImageOptions{Root: root, App: "todo", Push: tc.push}
			if tc.explicit {
				o.Targets = []string{"api"}
			}
			images, err := BuildImages(t.Context(), runner, o)
			if err != nil {
				t.Fatal(err)
			}
			if len(images) != 1 || len(runner.calls) != 1 {
				t.Fatalf("images=%v calls=%v", images, runner.calls)
			}
			c := runner.calls[0]
			i := slices.Index(c.Args, "--build-context")
			if i < 0 || c.Args[i+1] != "web=web" || c.Dir != root || c.Args[len(c.Args)-1] != "api" {
				t.Fatalf("command=%#v", c)
			}
			if tc.push && images[0].Digest != "sha256:api" {
				t.Fatal(images)
			}
		})
	}
}

type embedRunner struct {
	fake   run.Fake
	before func(run.Cmd) error
}

func (r *embedRunner) Run(ctx context.Context, c run.Cmd) (run.Result, error) {
	if r.before != nil {
		if err := r.before(c); err != nil {
			return run.Result{}, err
		}
	}
	return r.fake.Run(ctx, c)
}

func TestEmbedLocal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, manager, target, fail string
		dry                         bool
	}{
		{name: "pnpm", manager: "pnpm"}, {name: "bun all", manager: "bun", target: "all"},
		{name: "dry", manager: "pnpm", dry: true},
		{name: "install failure", manager: "pnpm", fail: "install"},
		{name: "web failure", manager: "bun", fail: "web"},
		{name: "missing output", manager: "pnpm", fail: "copy"},
		{name: "go failure", manager: "pnpm", fail: "go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := embedProject(t, tc.manager)
			writeBuildFile(t, root, "api/internal/webdist/dist/old.js", "stale")
			if tc.fail != "copy" {
				writeBuildFile(t, root, "web/build/_app/immutable/start.js", "new asset")
			}
			installArgs := []string{"--dir", "web", "install", "--frozen-lockfile"}
			webArgs := []string{"--dir", "web", "build"}
			webDir := root
			if tc.manager == "bun" {
				installArgs = []string{"install", "--frozen-lockfile"}
				webArgs = []string{"run", "build"}
				webDir = filepath.Join(root, "web")
			}
			goArgs := []string{"build", "-tags", "embedweb", "-trimpath", "-ldflags", "-X main.version=v1", "-o", "bin/api", "./cmd/api"}
			runner := &embedRunner{}
			if !tc.dry {
				install := runner.fake.Expect(tc.manager, installArgs...)
				if tc.fail == "install" {
					install.Fail(errors.New("install failed"))
				} else {
					web := runner.fake.Expect(tc.manager, webArgs...)
					if tc.fail == "web" {
						web.Fail(errors.New("web failed"))
					} else if tc.fail != "copy" {
						build := runner.fake.Expect("go", goArgs...)
						if tc.fail == "go" {
							build.Fail(errors.New("go failed"))
						}
					}
				}
			}
			runner.before = func(c run.Cmd) error {
				if c.Name != "go" {
					if c.Dir != webDir {
						t.Errorf("web directory=%q, want %q", c.Dir, webDir)
					}
					if reflect.DeepEqual(c.Args, webArgs) && tc.fail == "" {
						writeBuildFile(t, root, "web/build/index.html", "fresh build")
					}
					return nil
				}
				if c.Dir != filepath.Join(root, "api") {
					t.Errorf("go directory=%q", c.Dir)
				}
				if _, err := os.Stat(filepath.Join(root, "api/internal/webdist/dist/old.js")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("old asset remains: %v", err)
				}
				data, err := os.ReadFile(filepath.Join(root, "api/internal/webdist/dist/_app/immutable/start.js"))
				if err != nil || string(data) != "new asset" {
					t.Errorf("copied asset=%q, %v", data, err)
				}
				if tc.fail == "" {
					data, err := os.ReadFile(filepath.Join(root, "api/internal/webdist/dist/index.html"))
					if err != nil || string(data) != "fresh build" {
						t.Errorf("copied index=%q, %v", data, err)
					}
				}
				return nil
			}
			var out bytes.Buffer
			err := Local(t.Context(), Options{Dir: root, Target: tc.target, Tag: "v1", DryRun: tc.dry, Out: &out, ErrOut: &out}, runner)
			if tc.fail != "" {
				if err == nil || !strings.Contains(errorText(err), "Try:") {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			runner.fake.AssertDone(t)
			if tc.dry {
				if len(runner.fake.Calls()) != 0 {
					t.Fatal("dry run executed a command")
				}
				for _, want := range []string{"pnpm --dir web install --frozen-lockfile", "pnpm --dir web build", "web/build", "api/internal/webdist/dist", "go build -tags embedweb"} {
					if !strings.Contains(out.String(), want) {
						t.Errorf("output lacks %q: %s", want, &out)
					}
				}
				data, err := os.ReadFile(filepath.Join(root, "api/internal/webdist/dist/old.js"))
				if err != nil || string(data) != "stale" {
					t.Fatalf("dry run changed assets: %q, %v", data, err)
				}
			}
		})
	}
}

func TestNodeBuildCommands(t *testing.T) {
	t.Parallel()
	for _, manager := range []string{"pnpm", "bun"} {
		t.Run(manager, func(t *testing.T) {
			t.Parallel()
			root := embedProject(t, manager)
			lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
			if err != nil {
				t.Fatal(err)
			}
			lock.Answers["frontend_mode"] = "node"
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			fake := &run.Fake{}
			want := []string{"build", "-trimpath", "-ldflags", "-X main.version=dev", "-o", "bin/api", "./cmd/api"}
			fake.Expect("go", want...)
			if err := Local(t.Context(), Options{Dir: root}, fake); err != nil {
				t.Fatal(err)
			}
			fake.AssertDone(t)
			if calls := fake.Calls(); len(calls) != 1 || !reflect.DeepEqual(calls[0].Args, want) {
				t.Fatal(calls)
			}
			images := &metadataRunner{}
			if _, err := BuildImages(t.Context(), images, ImageOptions{Root: root, App: "todo"}); err != nil {
				t.Fatal(err)
			}
			if len(images.calls) != 2 {
				t.Fatal(images.calls)
			}
			for _, c := range images.calls {
				if slices.Contains(c.Args, "--build-context") {
					t.Fatal(c)
				}
			}
		})
	}
}

func TestInvalidBuildLock(t *testing.T) {
	t.Parallel()
	for _, image := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "image"}[image], func(t *testing.T) {
			t.Parallel()
			root := embedProject(t, "pnpm")
			writeBuildFile(t, root, "spinup.lock", "invalid JSON")
			fake := &run.Fake{}
			err := Local(t.Context(), Options{Dir: root, Image: image}, fake)
			if err == nil || !strings.Contains(errorText(err), "spinup.lock") || !strings.Contains(errorText(err), "Try:") {
				t.Fatalf("error=%v", err)
			}
			if len(fake.Calls()) != 0 {
				t.Fatal(fake.Calls())
			}
		})
	}
}
