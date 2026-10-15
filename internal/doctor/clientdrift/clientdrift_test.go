package clientdrift

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

type writingRunner struct {
	t            *testing.T
	root, output string
	fake         exec.Fake
	temp         string
}

func (r *writingRunner) Run(ctx context.Context, c exec.Cmd) (exec.Result, error) {
	r.t.Helper()
	if c.Dir != r.root || c.Name != "pnpm" || len(c.Args) != 7 || !reflect.DeepEqual(c.Args[:6], []string{"--dir", "web", "exec", "openapi-typescript", "../api/openapi/bundle.gen.yaml", "-o"}) || !filepath.IsAbs(c.Args[6]) {
		r.t.Fatalf("command = %+v", c)
	}
	r.temp = c.Args[6]
	if err := os.WriteFile(r.temp, []byte(r.output), 0600); err != nil {
		r.t.Fatal(err)
	}
	return r.fake.Run(ctx, c)
}

func TestCheck(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                                    string
		web, bundle, modules, missing, toolFail bool
		disk, fresh                             string
		status                                  doctor.Status
		message                                 string
	}{
		{name: "current", web: true, bundle: true, modules: true, disk: "types\n", fresh: "types\n", status: doctor.OK},
		{name: "line endings", web: true, bundle: true, modules: true, disk: "types\r\nnext\r\n", fresh: "types\nnext\n", status: doctor.OK},
		{name: "stale", web: true, bundle: true, modules: true, disk: "old\n", fresh: "new\n", status: doctor.Warn, message: "stale"},
		{name: "missing schema", web: true, bundle: true, modules: true, missing: true, status: doctor.Warn, message: "schema.d.ts"},
		{name: "no web", status: doctor.Skip, message: "no web pack"},
		{name: "no bundle", web: true, modules: true, status: doctor.Skip, message: "bundle.gen.yaml"},
		{name: "no modules", web: true, bundle: true, status: doctor.Skip, message: "web/node_modules"},
		{name: "tool fails", web: true, bundle: true, modules: true, toolFail: true, status: doctor.Warn, message: "pnpm"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			lock := manifest.New()
			if tt.web {
				lock.Packs = []string{"base", "api", "web"}
			}
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			if tt.bundle {
				write(t, root, "api/openapi/bundle.gen.yaml", "bundle")
			}
			if tt.modules {
				if err := os.MkdirAll(filepath.Join(root, "web/node_modules"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if !tt.missing {
				write(t, root, "web/src/lib/api/schema.d.ts", tt.disk)
			}
			r := &writingRunner{t: t, root: root, output: tt.fresh}
			if tt.web && tt.bundle && tt.modules {
				e := r.fake.Expect("pnpm", "--dir", "web", "exec", "openapi-typescript")
				if tt.toolFail {
					e.Fail(&exec.NotFoundError{Name: "pnpm"})
				}
			}
			got := runCheck(context.Background(), &doctor.Context{Root: root, Deps: map[string]any{"runner": r}})
			if got.Status != tt.status || !strings.Contains(got.Message, tt.message) {
				t.Fatalf("result = %+v", got)
			}
			if got.Status == doctor.Warn && got.Try != "spinup g client" {
				t.Fatalf("Try = %q", got.Try)
			}
			r.fake.AssertDone(t)
			if r.temp != "" {
				if _, err := os.Stat(r.temp); !os.IsNotExist(err) {
					t.Fatalf("temporary output remains: %v", err)
				}
			}
			if !tt.missing {
				disk, err := os.ReadFile(filepath.Join(root, "web/src/lib/api/schema.d.ts"))
				if err != nil || string(disk) != tt.disk {
					t.Fatalf("schema changed: %q, %v", disk, err)
				}
			}
		})
	}
}

func TestRegistrationAndOutsideProject(t *testing.T) {
	t.Parallel()
	found := false
	for _, c := range doctor.Checks(doctor.Local, false) {
		if c.ID == "client-drift" {
			found = true
			if c.Quick {
				t.Fatal("check is quick")
			}
			if got := c.Run(context.Background(), &doctor.Context{}); got.Status != doctor.Skip {
				t.Fatalf("outside project: %+v", got)
			}
		}
	}
	if !found {
		t.Fatal("check is not registered")
	}
	if got := runCheck(context.Background(), &doctor.Context{Root: t.TempDir(), Quick: true}); got.Status != doctor.Skip {
		t.Fatalf("quick: %+v", got)
	}
	if got := runCheck(context.Background(), &doctor.Context{Root: t.TempDir()}); got.Status != doctor.Skip {
		t.Fatalf("no lock: %+v", got)
	}
}

func write(t *testing.T, root, path, data string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

type variantRunner struct {
	fake exec.Fake
}

func (r *variantRunner) Run(ctx context.Context, c exec.Cmd) (exec.Result, error) {
	result, err := r.fake.Run(ctx, c)
	if err != nil {
		return result, err
	}
	output := c.Args[len(c.Args)-1]
	if strings.Contains(c.String(), "openapi-typescript") {
		err = os.WriteFile(output, []byte("schema\n"), 0600)
	} else {
		err = os.MkdirAll(filepath.Join(output, "nested"), 0700)
		if err == nil {
			err = os.WriteFile(filepath.Join(output, "nested/client.ts"), []byte("fresh\n"), 0600)
		}
	}
	return result, err
}

func TestHeyAPIDrift(t *testing.T) {
	t.Parallel()
	for _, pm := range []string{"pnpm", "bun"} {
		for _, difference := range []string{"none", "nested", "schema", "extra", "missing"} {
			t.Run(pm+"/"+difference, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				lock := manifest.New()
				lock.Packs = []string{"web"}
				lock.Answers = map[string]any{"package_manager": pm, "api_client": "heyapi"}
				if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
					t.Fatal(err)
				}
				write(t, root, "api/openapi/bundle.gen.yaml", "bundle")
				if err := os.MkdirAll(filepath.Join(root, "web/node_modules"), 0755); err != nil {
					t.Fatal(err)
				}
				clientData, schemaData := "fresh\r\n", "schema\r\n"
				if difference == "nested" {
					clientData = "stale"
				}
				if difference == "schema" {
					schemaData = "stale"
				}
				if difference != "missing" {
					write(t, root, "web/src/lib/api/client/nested/client.ts", clientData)
				}
				write(t, root, "web/src/lib/api/schema.d.ts", schemaData)
				if difference == "extra" {
					write(t, root, "web/src/lib/api/client/obsolete.ts", "stale")
				}
				runner := &variantRunner{}
				name, prefix := "pnpm", []string{"--dir", "web", "exec"}
				if pm == "bun" {
					name = "bunx"
					prefix = []string{"--no-install"}
				}
				runner.fake.Expect(name, append(append([]string{}, prefix...), "openapi-ts")...)
				runner.fake.Expect(name, append(append([]string{}, prefix...), "openapi-typescript")...)
				got := runCheck(t.Context(), &doctor.Context{Root: root, Deps: map[string]any{"runner": runner}})
				want := doctor.Warn
				if difference == "none" {
					want = doctor.OK
				}
				if got.Status != want {
					t.Fatalf("result = %+v, want %s", got, want)
				}
				runner.fake.AssertDone(t)
				for _, call := range runner.fake.Calls() {
					output := call.Args[len(call.Args)-1]
					if !filepath.IsAbs(output) {
						t.Fatalf("output is relative: %s", output)
					}
					if _, err := os.Stat(output); !os.IsNotExist(err) {
						t.Fatalf("temporary output remains: %v", err)
					}
				}
			})
		}
	}
}
