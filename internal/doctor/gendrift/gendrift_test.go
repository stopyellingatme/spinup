package gendrift

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen/openapi"
	"go.yaml.in/yaml/v3"
)

type toolRunner struct {
	t                 *testing.T
	root, fresh, temp string
	result            exec.Result
	err               error
	calls             int
}

func (r *toolRunner) Run(_ context.Context, c exec.Cmd) (exec.Result, error) {
	r.t.Helper()
	r.calls++
	if c.Dir != filepath.Join(r.root, "api") || c.Name != "go" || len(c.Args) < 4 || !reflect.DeepEqual(c.Args[:2], []string{"tool", "-modfile=tools.mod"}) {
		r.t.Fatalf("command = %+v", c)
	}
	switch c.Args[2] {
	case "oapi-codegen":
		if len(c.Args) != 6 || c.Args[3] != "-config" || c.Args[5] != "openapi/bundle.gen.yaml" {
			r.t.Fatalf("arguments = %v", c.Args)
		}
		r.temp = filepath.Dir(c.Args[4])
		if !filepath.IsAbs(r.temp) || strings.HasPrefix(r.temp, r.root+string(filepath.Separator)) {
			r.t.Fatalf("temporary config is inside app or relative: %s", r.temp)
		}
		data, err := os.ReadFile(c.Args[4])
		if err != nil {
			r.t.Fatal(err)
		}
		var config struct {
			Output   string          `yaml:"output"`
			Package  string          `yaml:"package"`
			Generate map[string]bool `yaml:"generate"`
		}
		if err := yaml.Unmarshal(data, &config); err != nil {
			r.t.Fatal(err)
		}
		if config.Package != "gen" || !config.Generate["models"] || filepath.Dir(config.Output) != r.temp {
			r.t.Fatalf("temporary config = %+v", config)
		}
		if r.err == nil && r.result.ExitCode == 0 {
			if err := os.WriteFile(config.Output, []byte(r.fresh), 0600); err != nil {
				r.t.Fatal(err)
			}
		}
	case "sqlc":
		if !reflect.DeepEqual(c.Args[3:], []string{"diff"}) {
			r.t.Fatalf("arguments = %v", c.Args)
		}
	default:
		r.t.Fatalf("unexpected tool: %+v", c)
	}
	return r.result, r.err
}

func write(t *testing.T, root, name, data string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func app(t *testing.T, paths bool) string {
	t.Helper()
	root := t.TempDir()
	spec := "openapi: 3.0.3\ninfo: {title: Test, version: '1'}\npaths: {}\n"
	if paths {
		spec = "openapi: 3.0.3\ninfo: {title: Test, version: '1'}\npaths:\n  /ping:\n    get:\n      responses:\n        '204': {description: OK}\n"
	}
	write(t, root, "api/openapi/openapi.yaml", spec)
	bundle, err := openapi.Bundle(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "api/openapi/bundle.gen.yaml", string(bundle))
	write(t, root, "api/oapi-codegen.yaml", "package: gen\noutput: custom/types.go\ngenerate:\n  models: true\n")
	write(t, root, "api/custom/types.go", "package gen\n")
	write(t, root, "api/db/queries/nested/query.sql", "-- name: Ping :one\nSELECT 1;\n")
	write(t, root, "api/sqlc.yaml", "version: '2'\n")
	return root
}

func TestOpenAPI(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"current", "empty", "new app", "stale bundle", "missing bundle", "stale types", "missing types", "invalid source", "missing config", "missing tool", "missing cache", "tool error"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := app(t, name != "empty" && name != "new app")
			runner := &toolRunner{t: t, root: root, fresh: "package gen\n"}
			want, calls, message := doctor.Fail, 0, ""
			switch name {
			case "current":
				want, calls = doctor.OK, 1
			case "empty":
				want = doctor.OK
			case "new app":
				// A new app has no routes, and no generator has written the bundle yet.
				want = doctor.Skip
				if err := os.Remove(filepath.Join(root, "api/openapi/bundle.gen.yaml")); err != nil {
					t.Fatal(err)
				}
			case "stale bundle":
				write(t, root, "api/openapi/bundle.gen.yaml", "stale\n")
				message = "bundle.gen.yaml"
			case "missing bundle":
				if err := os.Remove(filepath.Join(root, "api/openapi/bundle.gen.yaml")); err != nil {
					t.Fatal(err)
				}
			case "stale types":
				calls = 1
				write(t, root, "api/custom/types.go", "old\n")
				message = "custom/types.go"
			case "missing types":
				calls = 1
				if err := os.Remove(filepath.Join(root, "api/custom/types.go")); err != nil {
					t.Fatal(err)
				}
			case "invalid source":
				write(t, root, "api/openapi/openapi.yaml", "[")
				message = "bundle"
			case "missing config":
				if err := os.Remove(filepath.Join(root, "api/oapi-codegen.yaml")); err != nil {
					t.Fatal(err)
				}
			case "missing tool":
				want, calls = doctor.Skip, 1
				runner.err = &exec.NotFoundError{Name: "go"}
				message = "go"
			case "missing cache":
				want, calls = doctor.Skip, 1
				runner.result = exec.Result{ExitCode: 1, Stderr: []byte("go: module lookup disabled by GOPROXY=off")}
				runner.err = &exec.ExitError{Code: 1}
				message = "GOPROXY=off"
			case "tool error":
				calls = 1
				runner.result = exec.Result{ExitCode: 1, Stderr: []byte("invalid operation")}
				runner.err = &exec.ExitError{Code: 1}
				message = "invalid operation"
			}
			before := tree(t, root)
			got := openAPICheck(context.Background(), &doctor.Context{Root: root, Deps: map[string]any{"runner": runner}})
			if got.Status != want || !strings.Contains(got.Message, message) {
				t.Fatalf("result = %+v, want %v containing %q", got, want, message)
			}
			if want == doctor.Fail && got.Try != "spinup g openapi" {
				t.Fatalf("Try = %q", got.Try)
			}
			if runner.calls != calls {
				t.Fatalf("calls = %d, want %d", runner.calls, calls)
			}
			if !reflect.DeepEqual(before, tree(t, root)) {
				t.Fatal("check changed the app")
			}
			if runner.temp != "" {
				if _, err := os.Stat(runner.temp); !os.IsNotExist(err) {
					t.Fatalf("temporary directory remains: %v", err)
				}
			}
		})
	}
}

func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		result[path] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSQLC(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"current", "diff", "missing output", "missing config", "no queries", "missing tool", "missing cache", "tool error"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := app(t, false)
			runner := &toolRunner{t: t, root: root}
			want, calls, message := doctor.OK, 1, ""
			switch name {
			case "diff":
				want, message = doctor.Fail, "--- a/internal/store/query.sql.go"
				runner.result = exec.Result{ExitCode: 1, Stderr: []byte(message + "\n" + strings.Repeat("diff line\n", 25))}
				runner.err = &exec.ExitError{Code: 1}
			case "missing output":
				want = doctor.Fail
				runner.result.ExitCode = 1
				runner.err = &exec.ExitError{Code: 1}
			case "missing config":
				want, calls = doctor.Skip, 0
				if err := os.Remove(filepath.Join(root, "api/sqlc.yaml")); err != nil {
					t.Fatal(err)
				}
			case "no queries":
				want, calls = doctor.Skip, 0
				if err := os.RemoveAll(filepath.Join(root, "api/db/queries")); err != nil {
					t.Fatal(err)
				}
			case "missing tool":
				want = doctor.Skip
				runner.err = &exec.NotFoundError{Name: "go"}
				message = "go"
			case "missing cache":
				want = doctor.Skip
				runner.result = exec.Result{ExitCode: 1, Stderr: []byte("go: module lookup disabled by GOPROXY=off")}
				runner.err = &exec.ExitError{Code: 1}
				message = "GOPROXY=off"
			case "tool error":
				want = doctor.Fail
				runner.result = exec.Result{ExitCode: 1, Stderr: []byte("invalid SQL")}
				runner.err = &exec.ExitError{Code: 1}
				message = "invalid SQL"
			}
			before := tree(t, root)
			got := sqlcCheck(context.Background(), &doctor.Context{Root: root, Deps: map[string]any{"runner": runner}})
			if got.Status != want || !strings.Contains(got.Message, message) {
				t.Fatalf("result = %+v, want %v containing %q", got, want, message)
			}
			if want == doctor.Fail && got.Try != "spinup db sqlc" {
				t.Fatalf("Try = %q", got.Try)
			}
			if name == "diff" && strings.Count(got.Message, "diff line") != 19 {
				t.Fatalf("diff was not limited to 20 lines: %s", got.Message)
			}
			if runner.calls != calls {
				t.Fatalf("calls = %d, want %d", runner.calls, calls)
			}
			if !reflect.DeepEqual(before, tree(t, root)) {
				t.Fatal("check changed the app")
			}
		})
	}
}

func TestRegistration(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"openapi-drift", "sqlc-drift"} {
		found := false
		for _, check := range doctor.Checks(doctor.Local, false) {
			if check.ID != id {
				continue
			}
			found = true
			if check.Quick {
				t.Fatalf("%s is quick", id)
			}
			for _, c := range []*doctor.Context{{}, {Root: t.TempDir()}, {Root: app(t, true), Quick: true}} {
				if got := check.Run(context.Background(), c); got.Status != doctor.Skip {
					t.Fatalf("%s: %+v", id, got)
				}
			}
		}
		if !found {
			t.Fatalf("%s is not registered", id)
		}
	}
}
