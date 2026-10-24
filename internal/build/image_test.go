package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	run "github.com/stopyellingatme/spinup/internal/exec"
)

func TestBuildImages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		push     bool
		registry string
		wantRef  string
		wantArgs []string
	}{
		{
			name:     "push",
			push:     true,
			wantRef:  "ghcr.io/acme/todo-api:v1",
			wantArgs: []string{"buildx", "build", "--platform", "linux/amd64", "-f", "deploy/Dockerfile.api", "-t", "ghcr.io/acme/todo-api:v1", "--build-arg", "VERSION=v1", "--provenance=mode=max", "--sbom=true"},
		},
		{
			name:     "registry override",
			push:     true,
			registry: "registry.example",
			wantRef:  "registry.example/acme/todo-api:v1",
			wantArgs: []string{"buildx", "build", "--platform", "linux/amd64", "-f", "deploy/Dockerfile.api", "-t", "registry.example/acme/todo-api:v1", "--build-arg", "VERSION=v1", "--provenance=mode=max", "--sbom=true"},
		},
		{
			name:     "load",
			wantRef:  "todo-api:v1",
			wantArgs: []string{"buildx", "build", "--platform", "linux/amd64", "-f", "deploy/Dockerfile.api", "-t", "todo-api:v1", "--build-arg", "VERSION=v1", "--load", "api"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := imageProject(t, "module github.com/acme/todo/api\n")
			runner := &metadataRunner{digest: "sha256:api"}
			images, err := BuildImages(context.Background(), runner, ImageOptions{Root: root, App: "todo", Tag: "v1", Push: tc.push, Registry: tc.registry, PublicOrigin: "https://todo.example"})
			if err != nil {
				t.Fatal(err)
			}
			if len(images) != 2 || images[0].Ref != tc.wantRef || images[0].Digest != map[bool]string{true: "sha256:api", false: ""}[tc.push] {
				t.Fatalf("images = %#v", images)
			}
			calls := runner.calls
			if len(calls) != 2 {
				t.Fatalf("calls = %#v", calls)
			}
			if !reflect.DeepEqual(calls[0].Args[:len(tc.wantArgs)], tc.wantArgs) {
				t.Fatalf("api args = %#v", calls[0].Args)
			}
			if tc.push {
				if calls[0].Args[len(calls[0].Args)-2] != "--push" || calls[0].Args[len(calls[0].Args)-1] != "api" {
					t.Fatalf("api args = %#v", calls[0].Args)
				}
			} else if !reflect.DeepEqual(calls[0].Args, tc.wantArgs) {
				t.Fatalf("api args = %#v", calls[0].Args)
			}
			if calls[1].Args[5] != "deploy/Dockerfile.web" || calls[1].Args[9] != "PUBLIC_ORIGIN=https://todo.example" || calls[1].Args[len(calls[1].Args)-1] != "web" {
				t.Fatalf("web args = %#v", calls[1].Args)
			}
		})
	}
}

func imageProject(t *testing.T, mod string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "api"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "go.mod"), []byte(mod), 0644); err != nil {
		t.Fatal(err)
	}
	// A web app has deploy/Dockerfile.web. TestBuildImagesAPIOnly removes it.
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "deploy", "Dockerfile.web"), []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestBuildImagesAPIOnly(t *testing.T) {
	t.Parallel()
	root := imageProject(t, "module github.com/acme/todo/api\n")
	if err := os.Remove(filepath.Join(root, "deploy", "Dockerfile.web")); err != nil {
		t.Fatal(err)
	}
	images := &metadataRunner{}
	got, err := BuildImages(t.Context(), images, ImageOptions{Root: root, App: "todo", Tag: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "todo-api" || len(images.calls) != 1 {
		t.Fatalf("an app without deploy/Dockerfile.web builds only the API image: %+v, %d calls", got, len(images.calls))
	}
}

type metadataRunner struct {
	digest string
	calls  []run.Cmd
}

func (r *metadataRunner) Run(_ context.Context, c run.Cmd) (run.Result, error) {
	r.calls = append(r.calls, c)
	for index, arg := range c.Args {
		if arg == "--metadata-file" && r.digest != "" {
			data, err := json.Marshal(map[string]string{"containerimage.digest": r.digest})
			if err != nil {
				return run.Result{}, err
			}
			if err := os.WriteFile(c.Args[index+1], data, 0600); err != nil {
				return run.Result{}, err
			}
		}
	}
	return run.Result{}, nil
}
