package deploy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/remote"
)

// These tests run the exact remote scripts of the flow against a local directory tree.

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func run(t *testing.T, vm localVM, argv ...string) string {
	t.Helper()
	result, err := vm.Run(context.Background(), remote.Cmd{Argv: argv})
	if err != nil {
		t.Fatalf("%v: %v", argv, err)
	}
	return string(result.Stdout)
}

func TestShellEdgeSync(t *testing.T) {
	t.Parallel()
	vm := localVM{root: t.TempDir()}
	rel := "/opt/app/releases/" + testRelease
	writeTree(t, vm.root, map[string]string{
		"releases/" + testRelease + "/Caddyfile":                    "new caddyfile\n",
		"releases/" + testRelease + "/Caddyfile.d/a.caddy":          "a\n",
		"releases/" + testRelease + "/postgres/postgresql.conf":     "max_connections = 200\n",
		"releases/" + testRelease + "/postgres/conf.d/archive.conf": "archive_mode = on\n",
		"caddy/Caddyfile":               "old caddyfile\n",
		"caddy/Caddyfile.d/stale.caddy": "stale\n",
		"caddy/upstream.caddy":          "live\n",
	})
	for range 2 { // The step is idempotent.
		run(t, vm, "sh", "-c", edgeSyncScript, "spinup-edge", "/opt/app", rel)
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(vm.root, name))
		if err != nil {
			return "<missing>"
		}
		return string(data)
	}
	for name, want := range map[string]string{
		"caddy/Caddyfile":               "old caddyfile\n", // replaced only after validation
		"caddy/Caddyfile.next":          "new caddyfile\n",
		"caddy/Caddyfile.d/a.caddy":     "a\n",
		"caddy/Caddyfile.d/stale.caddy": "<missing>",
		"caddy/upstream.caddy":          "live\n",
		"postgres/postgresql.conf":      "max_connections = 200\n",
		"postgres/conf.d/archive.conf":  "archive_mode = on\n",
	} {
		if got := read(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, dir := range []string{"data/caddy/data", "data/caddy/config"} {
		if info, err := os.Stat(filepath.Join(vm.root, dir)); err != nil || !info.IsDir() {
			t.Errorf("%s was not created", dir)
		}
	}
	// A release without Caddyfile.d gives an empty directory, not a stale one.
	if err := os.RemoveAll(filepath.Join(vm.root, "releases", testRelease, "Caddyfile.d")); err != nil {
		t.Fatal(err)
	}
	run(t, vm, "sh", "-c", edgeSyncScript, "spinup-edge", "/opt/app", rel)
	if entries, err := os.ReadDir(filepath.Join(vm.root, "caddy", "Caddyfile.d")); err != nil || len(entries) != 0 {
		t.Fatalf("Caddyfile.d = %v, %v", entries, err)
	}
}

func TestShellReadScripts(t *testing.T) {
	t.Parallel()
	vm := localVM{root: t.TempDir()}
	ctx := context.Background()
	if got, err := ReadUpstream(ctx, vm, "app"); got != nil || err != nil {
		t.Fatalf("missing upstream = %q, %v", got, err)
	}
	if got, err := readRelease(ctx, vm, "app", ""); got != nil || err != nil {
		t.Fatalf("missing release = %#v, %v", got, err)
	}
	if got, err := ReadReleases(ctx, vm, "app"); got != nil || err != nil {
		t.Fatalf("missing releases.json = %#v, %v", got, err)
	}
	if got := run(t, vm, "sh", "-c", readCurrentScript, "spinup-current", "/opt/app/current"); got != "" {
		t.Fatalf("missing current = %q", got)
	}
	if got := run(t, vm, "sh", "-c", listReleasesScript, "spinup-releases", "/opt/app"); got != "" {
		t.Fatalf("empty releases = %q", got)
	}
	writeTree(t, vm.root, map[string]string{
		"caddy/upstream.caddy":                             string(UpstreamFile(Green)),
		"releases/" + testRelease + "/compose.yml":         "x\n",
		"releases/" + testRelease + "/Caddyfile.d/a.caddy": "a\n",
		"releases/" + testRelease + "/release.json":        `{"release":"` + testRelease + `","color":"green"}`,
		"releases/stray-file":                              "",
	})
	for range 2 {
		run(t, vm, "ln", "-sfn", "releases/"+testRelease, "/opt/app/current")
	}
	if target, err := os.Readlink(filepath.Join(vm.root, "current")); err != nil || target != "releases/"+testRelease {
		t.Fatalf("current = %q, %v", target, err)
	}
	if got, err := ReadUpstream(ctx, vm, "app"); err != nil || LiveColor(got) != Green {
		t.Fatalf("upstream = %q, %v", got, err)
	}
	if got, err := readRelease(ctx, vm, "app", ""); err != nil || got.Color != Green {
		t.Fatalf("current release = %#v, %v", got, err)
	}
	files, err := untar([]byte(run(t, vm, "sh", "-c", readCurrentScript, "spinup-current", "/opt/app/current")))
	if err != nil || string(files["compose.yml"]) != "x\n" || string(files["Caddyfile.d/a.caddy"]) != "a\n" {
		t.Fatalf("current tar = %v, %v", files, err)
	}
	if got := strings.Fields(run(t, vm, "sh", "-c", listReleasesScript, "spinup-releases", "/opt/app/releases")); !reflect.DeepEqual(got, []string{testRelease}) {
		t.Fatalf("releases = %v", got)
	}
}

func TestShellProbeAndRotate(t *testing.T) {
	t.Parallel()
	vm := localVM{root: t.TempDir()}
	ids := strings.Fields(run(t, vm, "sh", "-c", probeScript, "spinup-probe", "/opt/app"))
	if len(ids) != 2 || !isNumber(ids[0]) || !isNumber(ids[1]) {
		t.Fatalf("probe = %v", ids)
	}
	result, err := vm.Run(context.Background(), remote.Cmd{Argv: []string{"sh", "-c", probeScript, "spinup-probe", "/opt/app/missing"}})
	if err == nil || result.ExitCode != 3 {
		t.Fatalf("missing root = %d, %v", result.ExitCode, err)
	}
	state := "/opt/app/deploy.state"
	run(t, vm, "sh", "-c", rotateJournalScript, "spinup-journal", state)
	writeTree(t, vm.root, map[string]string{"deploy.state": "old\n"})
	run(t, vm, "sh", "-c", rotateJournalScript, "spinup-journal", state)
	if _, err := os.Stat(filepath.Join(vm.root, "deploy.state")); !os.IsNotExist(err) {
		t.Fatal("journal was not rotated")
	}
	if data, _ := os.ReadFile(filepath.Join(vm.root, "deploy.state.prev")); string(data) != "old\n" {
		t.Fatalf("previous journal = %q", data)
	}
}
