package render

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func fixtureStack() *stackversions.Stack {
	return &stackversions.Stack{
		Images:    map[string]stackversions.Image{"postgres": {Image: "postgres", Tag: "18", Digest: "sha256:abc"}},
		NPM:       map[string]string{"svelte": "5.0"},
		GoModules: map[string]string{"example.com/module": "v1.0.0"},
		Actions:   map[string]stackversions.Action{"owner/repo": {Tag: "v4", SHA: "abc"}},
	}
}

func fixturePack(name, output, source, content string) *templates.Pack {
	return &templates.Pack{Name: name, FS: fstest.MapFS{source: {Data: []byte(content)}}, Files: []templates.FileSpec{{Path: output, Source: source, Class: "managed"}}}
}

func TestTemplateFunctions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body, want string }{
		{"lower", `[[ lower "Hello É" ]]`, "hello é"},
		{"upper", `[[ upper "Hello é" ]]`, "HELLO É"},
		{"snake", `[[ snake "HTTPServer hello-world" ]]`, "http_server_hello_world"},
		{"kebab", `[[ kebab "HTTPServer hello_world" ]]`, "http-server-hello-world"},
		{"camel", `[[ camel "HTTP_server hello-world" ]]`, "httpServerHelloWorld"},
		{"pascal", `[[ pascal "HTTP_server hello-world" ]]`, "HttpServerHelloWorld"},
		{"quote", `[[ quote "a\n\"b" ]]`, `"a\n\"b"`},
		{"join", `[[ join "," .modules ]]`, "jobs,mail"},
		{"indent", `[[ indent 2 "a\nb" ]]`, "  a\n  b"},
		{"default empty", `[[ default "fallback" "" ]]`, "fallback"},
		{"default false", `[[ default "fallback" false ]]`, "fallback"},
		{"default zero", `[[ default "fallback" 0 ]]`, "fallback"},
		{"default value", `[[ default "fallback" "value" ]]`, "value"},
		{"image", `[[ image "postgres" ]]`, "postgres:18@sha256:abc"},
		{"npm", `[[ npm "svelte" ]]`, "5.0"},
		{"gomod", `[[ gomod "example.com/module" ]]`, "v1.0.0"},
		{"uses", `[[ uses "owner/repo" ]]`, "owner/repo@abc # v4"},
		{"has true", `[[ has "jobs" .modules ]]`, "true"},
		{"has false", `[[ has "auth" .modules ]]`, "false"},
		{"reserved data", `[[ .Stack.Go.Toolchain ]] [[ .TemplateVersion ]]`, " 0.1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := fixturePack("example", "result.txt", "file.tmpl", tc.body)
			plan, err := Render(Input{Packs: []*templates.Pack{p}, Answers: map[string]any{"modules": []string{"jobs", "mail"}}, Stack: fixtureStack(), TemplateVersion: "0.1.0"})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Files) != 1 || string(plan.Files[0].Data) != tc.want+"\n" {
				t.Fatalf("got %#v, want %q", plan.Files, tc.want+"\n")
			}
		})
	}
}

func TestRenderErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, output string
		nilStack           bool
		failure            string
	}{
		{name: "image lookup", body: `[[ image "missing" ]]`, failure: "missing"},
		{name: "npm lookup", body: `[[ npm "missing" ]]`, failure: "missing"},
		{name: "gomod lookup", body: `[[ gomod "missing" ]]`, failure: "missing"},
		{name: "uses lookup", body: `[[ uses "missing" ]]`, failure: "missing"},
		{name: "image nil", body: `[[ image "postgres" ]]`, nilStack: true, failure: "stack versions"},
		{name: "npm nil", body: `[[ npm "svelte" ]]`, nilStack: true, failure: "stack versions"},
		{name: "gomod nil", body: `[[ gomod "example.com/module" ]]`, nilStack: true, failure: "stack versions"},
		{name: "uses nil", body: `[[ uses "owner/repo" ]]`, nilStack: true, failure: "stack versions"},
		{name: "missing key", body: `[[ .missing ]]`, failure: "missing"},
		{name: "syntax", body: `[[ invalid ]]`, failure: "parse"},
		{name: "indent negative", body: `[[ indent -1 "text" ]]`, failure: "negative"},
		{name: "join type", body: `[[ join "," "text" ]]`, failure: "[]string"},
		{name: "has type", body: `[[ has "x" "text" ]]`, failure: "want a list"},
		{name: "go format", body: `package broken func`, output: "bad.go", failure: "format"},
		{name: "path traversal", body: "text", output: `[[ .unsafe ]]`, failure: "path"},
		{name: "path missing", body: "text", output: `[[ .missing ]]`, failure: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			output := tc.output
			if output == "" {
				output = "result.txt"
			}
			stack := fixtureStack()
			if tc.nilStack {
				stack = nil
			}
			p := fixturePack("example", output, "file.tmpl", tc.body)
			plan, err := Render(Input{Packs: []*templates.Pack{p}, Stack: stack, Answers: map[string]any{"unsafe": "../escape"}})
			if err == nil || !strings.Contains(err.Error(), tc.failure) || !strings.Contains(err.Error(), "example/file.tmpl") || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatalf("error: %v", err)
			}
			if len(plan.Files) != 0 {
				t.Fatal("partial plan returned")
			}
		})
	}
}

func TestSampleDeterminism(t *testing.T) {
	t.Parallel()
	p, err := templates.Load(templates.Embedded(), "_sample")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		answers map[string]any
		paths   []string
	}{
		{"default", nil, []string{"api/cmd/demo/main.txt", "versions.txt", "README.md", "database.txt", "scripts/start.sh", "greeting.txt"}},
		{"all", map[string]any{"app_name": "custom", "auth": true, "modules": []string{"jobs"}}, []string{"api/cmd/custom/main.txt", "versions.txt", "README.md", "database.txt", "auth.txt", "jobs.txt", "scripts/start.sh", "greeting.txt"}},
		{"no database", map[string]any{"db": "none"}, []string{"api/cmd/demo/main.txt", "versions.txt", "README.md", "scripts/start.sh", "greeting.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := Input{Packs: []*templates.Pack{p}, Answers: tc.answers, Stack: stackversions.MustLoad(), TemplateVersion: "0.1.0"}
			first, err := Render(in)
			if err != nil {
				t.Fatal(err)
			}
			second, err := Render(in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatal("plans differ")
			}
			var paths []string
			for _, f := range first.Files {
				paths = append(paths, f.Path)
			}
			if !reflect.DeepEqual(paths, tc.paths) {
				t.Fatalf("paths: %v", paths)
			}
			for _, f := range first.Files {
				if !strings.HasSuffix(string(f.Data), "\n") || bytes.Contains(f.Data, []byte("\r")) {
					t.Fatalf("not normalized: %s", f.Path)
				}
				if !strings.HasPrefix(f.Template, "sample/files/") {
					t.Fatalf("template: %s", f.Template)
				}
				if f.Path == "scripts/start.sh" {
					if f.Mode != 0o755 {
						t.Fatalf("mode: %o", f.Mode)
					}
				} else if f.Mode != 0o644 {
					t.Fatalf("mode: %o", f.Mode)
				}
			}
			sample := first.Files[0]
			if sample.Class != "scaffold" || !bytes.HasPrefix(sample.Data, []byte(`Application: "`)) {
				t.Fatalf("sample output: %s", sample.Data)
			}
			readme, ok := first.Get("README.md")
			if !ok || readme.Class != "seed" {
				t.Fatal("seed missing")
			}
			versions, ok := first.Get("versions.txt")
			if !ok || versions.Class != "managed" || !bytes.Contains(versions.Data, []byte("postgres:18")) {
				t.Fatal("managed versions missing")
			}
		})
	}
}

func TestCopyAndNormalize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, source, content, want string }{
		{"copied text", "plain", "a\r\nb\r\n\r\n", "a\nb\n"},
		{"copied actions", "plain", "[[ .missing ]]", "[[ .missing ]]\n"},
		{"binary", "plain", "\x00a\r\n", "\x00a\r\n"},
		{"empty", "plain", "", ""},
		{"rendered text", "plain.tmpl", "a\r\nb\r\n\r\n", "a\nb\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := fixturePack("example", "out.txt", tc.source, tc.content)
			plan, err := Render(Input{Packs: []*templates.Pack{p}})
			if err != nil {
				t.Fatal(err)
			}
			if string(plan.Files[0].Data) != tc.want {
				t.Fatalf("got %q", plan.Files[0].Data)
			}
		})
	}
}

func TestDuplicateOutput(t *testing.T) {
	t.Parallel()
	a := fixturePack("a", "[[ .name ]]", "one.tmpl", "first")
	b := fixturePack("b", "out.txt", "two.tmpl", "second")
	_, err := Render(Input{Packs: []*templates.Pack{a, b}, Answers: map[string]any{"name": "out.txt"}})
	if err == nil || !strings.Contains(err.Error(), "a/one.tmpl") || !strings.Contains(err.Error(), "b/two.tmpl") {
		t.Fatalf("error: %v", err)
	}
}
