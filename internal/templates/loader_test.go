package templates

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadFragments(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"example/pack.toml":     {Data: []byte("name = 'example'\n[[questions]]\nkey='app_name'\ntype='string'\ndefault=''\n[[files]]\npath='a.txt'\nsource='files/a'\nclass='seed'\n")},
		"example/files/a":       {Data: []byte("a")},
		"example/pack.d/b.toml": {Data: []byte("[[questions]]\nkey='second'\ntype='bool'\ndefault=false\n")},
		"example/pack.d/a.toml": {Data: []byte("[[questions]]\nkey='first'\ntype='enum'\nchoices=['yes','no']\n")},
	}
	p, err := Load(fsys, "example")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Questions) != 3 || p.Questions[1].Key != "first" || p.Questions[2].Default != "false" {
		t.Fatalf("questions: %#v", p.Questions)
	}
	if !p.Questions[0].HasDefault() {
		t.Fatal("empty default lost")
	}
	if _, err := fs.ReadFile(p.FS, "files/a"); err != nil {
		t.Fatal(err)
	}
	names, err := List(fsys)
	if err != nil || len(names) != 1 || names[0] != "example" {
		t.Fatalf("List: %v, %v", names, err)
	}
}

func TestLoadDuplicate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body string }{
		{"question", "[[questions]]\nkey='app_name'\ntype='string'\n"},
		{"file", "[[files]]\npath='a.txt'\nsource='files/a'\nclass='seed'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fsys := fstest.MapFS{
				"example/pack.toml":         {Data: []byte("name='example'\n[[questions]]\nkey='app_name'\ntype='string'\n[[files]]\npath='a.txt'\nsource='files/a'\nclass='seed'\n")},
				"example/pack.d/extra.toml": {Data: []byte(tc.body)},
				"example/files/a":           {Data: []byte("a")},
			}
			_, err := Load(fsys, "example")
			if err == nil || !strings.Contains(err.Error(), "example/pack.toml") || !strings.Contains(err.Error(), "example/pack.d/extra.toml") {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestConditions(t *testing.T) {
	t.Parallel()
	answers := map[string]any{"db": "postgres", "auth": false, "jobs": true, "modules": []string{"jobs"}}
	for _, tc := range []struct {
		expr    string
		want    bool
		failure string
	}{
		{"", true, ""}, {"db == 'postgres'", true, ""}, {"db != 'postgres'", false, ""},
		{"auth", false, ""}, {"!auth", true, ""}, {"modules contains 'jobs'", true, ""},
		{"jobs || auth && auth", true, ""}, {"auth && jobs || !auth", true, ""},
		{"jobs || missing", false, "unknown"}, {"auth && missing", false, "unknown"},
		{"db", false, "bool"}, {"auth == 'true'", false, "string"}, {"db contains 'jobs'", false, "slice"},
		{"(auth)", false, "invalid"}, {"db = 'x'", false, "invalid"}, {"auth &&", false, "invalid"},
		{"db == 'unterminated", false, "invalid"}, {"auth jobs", false, "invalid"},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			t.Parallel()
			f := FileSpec{When: tc.expr}
			got, err := f.Matches(answers)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("got %v, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestInvalidPacks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body, fragment, failure string }{
		{name: "wrong name", body: "name='other'", failure: "name must equal"},
		{name: "missing name", body: "description='test'", failure: "name must equal"},
		{name: "invalid TOML", body: "name=", failure: "decode"},
		{name: "unknown field", body: "name='example'\nunknown=true", failure: "decode"},
		{name: "fragment name", fragment: "name='example'", failure: "fragments"},
		{name: "fragment metadata", fragment: "requires=[]", failure: "fragments"},
		{name: "question type", body: "name='example'\n[[questions]]\nkey='x'\ntype='number'", failure: "invalid type"},
		{name: "question key", body: "name='example'\n[[questions]]\nkey='bad-key'\ntype='string'", failure: "invalid or reserved"},
		{name: "reserved key", body: "name='example'\n[[questions]]\nkey='Stack'\ntype='string'", failure: "reserved"},
		{name: "empty enum", body: "name='example'\n[[questions]]\nkey='x'\ntype='enum'", failure: "no choices"},
		{name: "number default", body: "name='example'\n[[questions]]\nkey='x'\ntype='string'\ndefault=2", failure: "default"},
		{name: "string bool default", body: "name='example'\n[[questions]]\nkey='x'\ntype='bool'\ndefault='not-bool'", failure: "bool default"},
		{name: "bool string default", body: "name='example'\n[[questions]]\nkey='x'\ntype='string'\ndefault=true", failure: "bool default"},
		{name: "bad pattern", body: "name='example'\n[[questions]]\nkey='x'\ntype='string'\npattern='['", failure: "invalid pattern"},
		{name: "bool pattern", body: "name='example'\n[[questions]]\nkey='x'\ntype='bool'\npattern='.'", failure: "pattern requires"},
		{name: "string choices", body: "name='example'\n[[questions]]\nkey='x'\ntype='string'\nchoices=['a']", failure: "choices require"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := tc.body
			if body == "" {
				body = "name='example'"
			}
			fsys := fstest.MapFS{"example/pack.toml": {Data: []byte(body)}}
			if tc.fragment != "" {
				fsys["example/pack.d/extra.toml"] = &fstest.MapFile{Data: []byte(tc.fragment)}
			}
			_, err := Load(fsys, "example")
			if err == nil || !strings.Contains(err.Error(), tc.failure) || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestValidateFile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		change  func(*FileSpec)
		failure string
	}{
		{"class", func(f *FileSpec) { f.Class = "other" }, "class"},
		{"empty path", func(f *FileSpec) { f.Path = "" }, "path"},
		{"absolute path", func(f *FileSpec) { f.Path = "/escape" }, "path"},
		{"parent path", func(f *FileSpec) { f.Path = "a/../escape" }, "path"},
		{"unclean path", func(f *FileSpec) { f.Path = "a//b" }, "path"},
		{"backslash path", func(f *FileSpec) { f.Path = `a\b` }, "path"},
		{"NUL path", func(f *FileSpec) { f.Path = "a\x00b" }, "path"},
		{"rename path", func(f *FileSpec) { f.RenamedFrom = "../old" }, "path"},
		{"source path", func(f *FileSpec) { f.Source = "../source" }, "path"},
		{"missing source", func(f *FileSpec) { f.Source = "missing" }, "source"},
		{"directory source", func(f *FileSpec) { f.Source = "files" }, "regular file"},
		{"condition", func(f *FileSpec) { f.When = "(auth)" }, "invalid when"},
		{"mode length", func(f *FileSpec) { f.Mode = "755" }, "mode"},
		{"mode digits", func(f *FileSpec) { f.Mode = "0999" }, "mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := FileSpec{Path: "out.txt", Source: "files/a", Class: "managed"}
			tc.change(&f)
			p := &Pack{Name: "example", FS: fstest.MapFS{"files/a": {Data: []byte("a")}}, Files: []FileSpec{f}}
			err := p.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.failure) {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestListAndEmbedded(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"z/pack.toml":   {Data: []byte("name='z'")},
		"a/pack.toml":   {Data: []byte("name='a'")},
		"not-pack/file": {Data: []byte("ignored")},
		"file":          {Data: []byte("ignored")},
	}
	names, err := List(fsys)
	if err != nil || strings.Join(names, ",") != "a,z" {
		t.Fatalf("got %v, %v", names, err)
	}
	names, err = List(Embedded())
	if err != nil || !strings.Contains(strings.Join(names, ","), "_sample") {
		t.Fatalf("got %v, %v", names, err)
	}
	p, err := Load(Embedded(), "_sample")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "sample" || len(p.Questions) != 5 || len(p.Files) != 8 {
		t.Fatalf("sample: %#v", p)
	}
	if p.Files[0].condition == nil {
		t.Fatal("conditions were not compiled")
	}
}

func TestLoadInvalidName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", ".", "_", "../escape", "a/b", `a\b`, "missing"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Load(fstest.MapFS{}, name); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if _, err := Load(nil, "sample"); err == nil {
		t.Fatal("nil FS accepted")
	}
	if _, err := List(nil); err == nil {
		t.Fatal("nil FS accepted")
	}
	if err := (*Pack)(nil).Validate(); err == nil {
		t.Fatal("nil pack accepted")
	}
}
