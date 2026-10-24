package build

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestLocal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, target string
		image, dry   bool
		replace      string
		wantErr      string
	}{
		{name: "api", target: "api"}, {name: "all", target: "all"}, {name: "web", target: "web", wantErr: "Try:"},
		{name: "image", target: "api", image: true}, {name: "image dry", target: "api", image: true, dry: true}, {name: "dry", target: "api", dry: true},
		{name: "replace", target: "api", image: true, replace: "replace github.com/stopyellingatme/spinup/kit => /tmp/kit", wantErr: "Try:"},
		{name: "replace block", target: "api", image: true, replace: "replace (\n github.com/stopyellingatme/spinup/kit v0.1.0 => ../kit\n)", wantErr: "Try:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for _, dir := range []string{"api", "deploy"} {
				if err := os.Mkdir(filepath.Join(root, dir), 0755); err != nil {
					t.Fatal(err)
				}
			}
			for name, data := range map[string]string{"spinup.toml": "[app]\nname = 'todo'\n", "api/go.mod": "module example.com/todo/api\n" + tc.replace + "\n", "deploy/Dockerfile.web": "FROM scratch\n"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0644); err != nil {
					t.Fatal(err)
				}
			}
			fake := &run.Fake{}
			want := run.Cmd{Name: "go", Args: []string{"build", "-trimpath", "-ldflags", "-X main.version=dev", "-o", "bin/api", "./cmd/api"}, Dir: filepath.Join(root, "api")}
			if tc.image {
				want.Name = "docker"
				want.Dir = root
				want.Args = []string{"buildx", "build", "--platform", "linux/amd64", "-f", "deploy/Dockerfile.api", "-t", "todo-api:dev", "--build-arg", "VERSION=dev", "--load", "api"}
			}
			if !tc.dry && tc.wantErr == "" {
				fake.Expect(want.Name, want.Args...)
				if tc.image {
					fake.Expect("docker", "buildx", "build", "--platform", "linux/amd64", "-f", "deploy/Dockerfile.web", "-t", "todo-web:dev", "--build-arg", "PUBLIC_ORIGIN=", "--load", "web")
				}
			}
			var out bytes.Buffer
			err := Local(context.Background(), Options{Dir: root, Target: tc.target, Image: tc.image, DryRun: tc.dry, Out: &out, ErrOut: &out}, fake)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(errorText(err), tc.wantErr) {
					t.Fatalf("error=%v", err)
				}
				if len(fake.Calls()) != 0 {
					t.Fatal("ran command")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			fake.AssertDone(t)
			if tc.dry {
				wantCommand := "go build"
				if tc.image {
					wantCommand = "docker buildx build"
				}
				if !strings.Contains(out.String(), wantCommand) {
					t.Fatal(out.String())
				}
				return
			}
			got := fake.Calls()[0]
			if got.Dir != want.Dir || !reflect.DeepEqual(got.Args, want.Args) {
				t.Fatalf("command=%#v", got)
			}
		})
	}
}

func errorText(err error) string {
	var ue *ui.Error
	if errors.As(err, &ue) {
		return err.Error() + "\nTry: " + ue.Try
	}
	return err.Error()
}

func TestLocalKitReplace(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mod string
		local     bool
	}{
		{"published", "replace github.com/stopyellingatme/spinup/kit => example.com/kit v0.2.0", false},
		{"quoted", "replace \"github.com/stopyellingatme/spinup/kit\" => \"/tmp/local kit\"", true},
		{"multiple versions", "replace (\n github.com/stopyellingatme/spinup/kit v0.1.0 => example.com/kit v0.2.0\n github.com/stopyellingatme/spinup/kit v0.3.0 => ../kit\n)", true},
		{"UNC", "replace github.com/stopyellingatme/spinup/kit => \"//server/kit\"", true},
		{"comment", "// replace github.com/stopyellingatme/spinup/kit => ../kit", false},
		{"other module", "replace example.com/other => ../kit", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := localKitReplace(tc.mod); got != tc.local {
				t.Fatalf("localKitReplace()=%v, want %v", got, tc.local)
			}
		})
	}
}

func TestMissingBuildTool(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	fake := &run.Fake{}
	fake.Expect("go").Fail(&run.NotFoundError{Name: "go"})
	err := Local(context.Background(), Options{Dir: root}, fake)
	if err == nil || !strings.Contains(errorText(err), "Try: install go") {
		t.Fatalf("error=%v", err)
	}
	fake.AssertDone(t)
}
