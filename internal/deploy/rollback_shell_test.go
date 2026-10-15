package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestRollbackConfigurationScript(t *testing.T) {
	t.Parallel()
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{true: "valid", false: "invalid"}[valid], func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTree(t, root, map[string]string{
				"release/.env":                     "SPINUP_COLOR=blue\nAPI_IMAGE=app-api:stored\n",
				"release/Caddyfile":                "stored config\n",
				"release/Caddyfile.d/stored.caddy": "stored fragment\n",
				"caddy/Caddyfile":                  "current config\n",
				"caddy/Caddyfile.d/current.caddy":  "current fragment\n",
				"caddy/upstream.caddy":             "live upstream\n",
				"postgres/postgresql.conf":         "database settings\n",
			})
			docker := filepath.Join(root, "docker")
			validate := "exit 1"
			if valid {
				validate = "exit 0"
			}
			if err := os.WriteFile(docker, []byte("#!/bin/sh\ncase $1 in\ncompose) echo caddy1;;\nexec) "+validate+";;\n*) exit 2;;\nesac\n"), 0700); err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(rollbackColorScript, "caddy=/opt/$3/caddy", "caddy="+remote.Quote(filepath.Join(root, "caddy")))
			script = strings.ReplaceAll(script, "docker ", remote.Quote(docker)+" ")
			_, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{"-c", script, "spinup-rollback", filepath.Join(root, "release"), Green, "app"}})
			if (err == nil) != valid {
				t.Fatal(err)
			}
			read := func(name string) string {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(root, name))
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
			want := "current config\n"
			fragment := "current"
			if valid {
				want = "stored config\n"
				fragment = "stored"
			}
			if got := read("caddy/Caddyfile"); got != want {
				t.Fatal(got)
			}
			if got := read("caddy/Caddyfile.d/" + fragment + ".caddy"); !strings.HasPrefix(got, fragment) {
				t.Fatal(got)
			}
			if got := read("release/.env"); got != "SPINUP_COLOR=green\nAPI_IMAGE=app-api:stored\n" {
				t.Fatal(got)
			}
			if got := read("caddy/upstream.caddy"); got != "live upstream\n" {
				t.Fatal(got)
			}
			if got := read("postgres/postgresql.conf"); got != "database settings\n" {
				t.Fatal(got)
			}
		})
	}
}
