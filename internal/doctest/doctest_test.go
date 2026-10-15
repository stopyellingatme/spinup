package doctest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/exec"
)

func TestParse(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("testdata", "parse.md"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := Parse("guide.md", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Blocks) != 3 || page.Blocks[0].RequiresEnv[0] != "DATABASE_URL" || page.Blocks[0].Steps[1].WantExit != 1 || !page.Blocks[1].Skip {
		t.Fatalf("Parse() = %#v", page)
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, input, want string }{
		{"unknown directive", "<!-- doctest: no -->\n```console\n$ true\n```", "guide.md:1: unknown doctest directive"},
		{"missing command", "```console\ntext\n```", "guide.md:1: console block has no command"},
		{"unclosed", "```console\n$ true", "guide.md:1: console fence is not closed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse("guide.md", []byte(test.input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMatch(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want []string
		got  string
		ok   bool
	}{
		{"exact", []string{"one", "two"}, "one\ntwo\n", true},
		{"leading ellipsis", []string{"...", "two"}, "one\ntwo\n", true},
		{"middle ellipsis", []string{"one", "...", "three"}, "one\ntwo\nthree\n", true},
		{"trailing ellipsis", []string{"one", "..."}, "one\ntwo\n", true},
		{"zero ellipsis", []string{"one", "...", "two"}, "one\ntwo\n", true},
		{"trim spaces", []string{"one"}, "one   \n", true},
		{"different", []string{"one"}, "two\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ok, _ := Match(test.want, test.got)
			if ok != test.ok {
				t.Fatalf("Match(%q, %q) = %v, want %v", test.want, test.got, ok, test.ok)
			}
		})
	}
}

func TestRunPage(t *testing.T) {
	t.Parallel()
	bin := t.TempDir()
	script := filepath.Join(bin, "example")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'hello %s\\n' \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	page, err := Parse("scratch.md", []byte("```console\n$ mkdir child\n$ cd child\n$ example world\nhello world\n$ sh -c 'echo broken; exit 7'\nbroken\n[exit 7]\n```"))
	if err != nil {
		t.Fatal(err)
	}
	if failures := RunPage(context.Background(), exec.OSRunner{}, page, Env{Bin: bin}); len(failures) != 0 {
		t.Fatalf("RunPage() = %#v", failures)
	}
}

func TestRunPageSkipsBlockAfterFailure(t *testing.T) {
	t.Parallel()
	page := Page{Path: "page.md", Blocks: []Block{{Steps: []Step{
		{Line: 1, Command: "echo wrong", Want: []string{"right"}},
		{Line: 2, Command: "false", WantExit: 1},
	}}, {Steps: []Step{{Line: 3, Command: "echo other", Want: []string{"wrong"}}}}}}
	failures := RunPage(context.Background(), exec.OSRunner{}, page, Env{})
	if len(failures) != 2 || failures[0].Line != 1 || failures[1].Line != 3 {
		t.Fatalf("RunPage() = %#v", failures)
	}
}

func TestDiscover(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"docs/a.md", "docs/nested/b.md", "docs/design/no.md", "docs/adr/no.md"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := Discover(root)
	if err != nil || strings.Join(paths, ",") != "docs/a.md,docs/nested/b.md" {
		t.Fatalf("Discover() = %q, %v", paths, err)
	}
}

// TestDocs runs every runnable console block in the repository documentation.
func TestDocs(t *testing.T) {
	root := repositoryRoot(t)
	paths, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("no doc pages")
	}
	bin := filepath.Join(t.TempDir(), "spinup")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if _, err := (exec.OSRunner{}).Run(ctx, exec.Cmd{Name: "go", Args: []string{"build", "-o", bin, "./cmd/spinup"}, Dir: root}); err != nil {
		t.Fatal(err)
	}
	// Pages run with their own HOME, so pin the Go caches to the real ones, and point
	// generated apps at the local kit (it is not published yet).
	goEnv, err := (exec.OSRunner{}).Run(ctx, exec.Cmd{Name: "go", Args: []string{"env", "GOCACHE", "GOMODCACHE"}, Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	caches := strings.Split(strings.TrimSpace(string(goEnv.Stdout)), "\n")
	if len(caches) != 2 {
		t.Fatalf("go env printed %q, want two lines", goEnv.Stdout)
	}
	vars := []string{
		"GOCACHE=" + caches[0],
		"GOMODCACHE=" + caches[1],
		"SPINUP_KIT_REPLACE=" + filepath.Join(root, "kit"),
	}
	// Pages run with their own HOME: pin the corepack cache and the pnpm store too.
	if home, err := os.UserHomeDir(); err == nil && os.Getenv("COREPACK_HOME") == "" {
		vars = append(vars, "COREPACK_HOME="+filepath.Join(home, ".cache", "node", "corepack"))
	}
	if store, err := (exec.OSRunner{}).Run(ctx, exec.Cmd{Name: "pnpm", Args: []string{"store", "path"}, Dir: root}); err == nil {
		vars = append(vars, "npm_config_store_dir="+strings.TrimSpace(string(store.Stdout)))
	}
	if value := os.Getenv("DATABASE_URL"); value != "" {
		vars = append(vars, "DATABASE_URL="+value)
	}
	for _, path := range paths {
		path := path
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
			if err != nil {
				t.Fatal(err)
			}
			page, err := Parse(path, data)
			if err != nil {
				t.Fatal(err)
			}
			for _, failure := range RunPage(ctx, exec.OSRunner{}, page, Env{Bin: filepath.Dir(bin), Vars: vars}) {
				t.Errorf("%s:%d: %s: %s", failure.Path, failure.Line, failure.Command, failure.Message)
			}
		})
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(directory, "go.mod"))
		if err == nil && strings.Contains(string(data), "module github.com/stopyellingatme/spinup") {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("repository root not found")
		}
		directory = parent
	}
}
