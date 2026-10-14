package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func tracked(class Class) FileEntry {
	return FileEntry{Class: class, Template: "api/main.go.tmpl", Hash: Hash([]byte("original")), Mode: "0644"}
}

func TestNewAndClasses(t *testing.T) {
	t.Parallel()
	l := New()
	if l.Schema != SchemaVersion || l.Answers == nil || l.StackVersions == nil || l.Files == nil {
		t.Fatalf("New() = %#v", l)
	}
	for _, class := range []Class{Managed, Scaffold, Seed, "", "other"} {
		if got := class.Valid(); got != (class == Managed || class == Scaffold || class == Seed) {
			t.Errorf("%q.Valid() = %v", class, got)
		}
	}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		edit func(*Lock)
		want string
	}{
		{"schema", func(l *Lock) { l.Schema = 0 }, "schema"},
		{"class", func(l *Lock) { l.Files["a"] = FileEntry{Class: "other"} }, "class"},
		{"empty hash", func(l *Lock) { e := tracked(Managed); e.Hash = ""; l.Files["a"] = e }, "hash"},
		{"uppercase hash", func(l *Lock) { e := tracked(Managed); e.Hash = "sha256:" + strings.Repeat("A", 64); l.Files["a"] = e }, "hash"},
		{"short hash", func(l *Lock) { e := tracked(Managed); e.Hash = "sha256:00"; l.Files["a"] = e }, "hash"},
		{"nonhex hash", func(l *Lock) { e := tracked(Managed); e.Hash = "sha256:" + strings.Repeat("g", 64); l.Files["a"] = e }, "hash"},
		{"mode", func(l *Lock) { e := tracked(Managed); e.Mode = "0688"; l.Files["a"] = e }, "mode"},
		{"no octal prefix", func(l *Lock) { e := tracked(Managed); e.Mode = "644"; l.Files["a"] = e }, "mode"},
		{"empty mode", func(l *Lock) { e := tracked(Managed); e.Mode = ""; l.Files["a"] = e }, "mode"},
		{"empty template", func(l *Lock) { e := tracked(Managed); e.Template = ""; l.Files["a"] = e }, "template"},
		{"template traversal", func(l *Lock) { e := tracked(Managed); e.Template = "../x"; l.Files["a"] = e }, "template"},
		{"live tombstone", func(l *Lock) { l.Files["a"] = tracked(Scaffold); l.Tombstones = []string{"a"} }, "also a live entry"},
		{"bad tombstone", func(l *Lock) { l.Tombstones = []string{"../a"} }, "tombstone"},
		{"bad orphan", func(l *Lock) { l.Orphans = []string{"/a"} }, "orphan"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := New()
			tt.edit(l)
			if err := l.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatalf("Validate() = %v, want %q and Try line", err, tt.want)
			}
		})
	}
	for _, path := range []string{"", ".", "..", "../a", "/a", "a/../b", "a/./b", "a//b", "a/", "a\\b", "C:/a", "a\x00b"} {
		t.Run("path "+path, func(t *testing.T) {
			t.Parallel()
			l := New()
			l.Files[path] = tracked(Managed)
			if err := l.Validate(); err == nil || !strings.Contains(err.Error(), "path") {
				t.Fatalf("Validate(%q) = %v", path, err)
			}
		})
	}
	l := New()
	l.Schema = 0
	l.Files["../a"] = FileEntry{Class: "other", Hash: "bad", Mode: "bad"}
	err := l.Validate()
	for _, want := range []string{"schema", "path", "class", "hash", "mode"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() = %v, missing %q", err, want)
		}
	}
}

func TestSeedAndModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"", "0000", "0644", "0755", "0777"} {
		l := New()
		l.Files["seed.sql"] = FileEntry{Class: Seed, Mode: mode}
		if err := l.Validate(); err != nil {
			t.Fatalf("seed mode %q: %v", mode, err)
		}
	}
	l := New()
	l.Files["seed.sql"] = FileEntry{Class: Seed, Hash: "bad"}
	if err := l.Validate(); err == nil {
		t.Fatal("invalid seed hash accepted")
	}
}

func TestMarshalDeterminism(t *testing.T) {
	t.Parallel()
	var want []byte
	for _, order := range [][]string{{"z", "a", "m"}, {"m", "z", "a"}, {"a", "m", "z"}} {
		l := New()
		l.FormatterGo = "go1.27.2"
		l.Kit = KitRef{Module: "example.com/kit", Version: "v1.0.0"}
		for _, key := range order {
			l.Files[key] = tracked(Managed)
			l.StackVersions[key] = key
			inner := make(map[string]any)
			for _, innerKey := range order {
				inner[innerKey] = innerKey
			}
			l.Answers[key] = []any{inner, json.Number("9007199254740993")}
			l.Plugins = append(l.Plugins, Plugin{Name: key, Version: "v1", SHA256: key, API: 1})
		}
		l.Tombstones = []string{"removed/z", "removed/a", "removed/z"}
		l.Orphans = []string{"orphan/z", "orphan/a", "orphan/z"}
		l.AppliedMigrations = []string{"0002", "0001", "0002"}
		before := append([]string(nil), l.Tombstones...)
		got, err := l.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(l.Tombstones, before) {
			t.Fatal("Marshal changed the lock")
		}
		parsed, err := Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		roundtrip, err := parsed.Marshal()
		if err != nil || !bytes.Equal(got, roundtrip) {
			t.Fatalf("roundtrip differs: %v\n%s\n%s", err, got, roundtrip)
		}
		if want != nil && !bytes.Equal(got, want) {
			t.Fatalf("insertion order changed output:\n%s\n%s", want, got)
		}
		want = got
		if !bytes.HasSuffix(got, []byte("\n")) || !bytes.Contains(got, []byte("\n  \"answers\":")) {
			t.Fatal("missing indentation or trailing newline")
		}
		if !reflect.DeepEqual(parsed.Tombstones, []string{"removed/a", "removed/z"}) ||
			!reflect.DeepEqual(parsed.Orphans, []string{"orphan/a", "orphan/z"}) ||
			!reflect.DeepEqual(parsed.AppliedMigrations, []string{"0001", "0002"}) {
			t.Fatal("slices are not sorted and deduplicated")
		}
		for i, key := range []string{"a", "m", "z"} {
			if parsed.Plugins[i].Name != key {
				t.Fatal("plugins are not sorted")
			}
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(got, &object); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"schema", "cli_version", "template_version", "min_cli", "kit", "behavior_version", "formatter_go", "answers", "stack_versions", "plugins", "files", "tombstones", "orphans", "applied_migrations"} {
			if _, ok := object[key]; !ok {
				t.Errorf("missing JSON key %q", key)
			}
		}
		// Struct objects must have sorted keys too.
		if !bytes.Contains(got, []byte("\"api\": 1,\n      \"name\":")) {
			t.Fatal("plugin object keys are not sorted")
		}
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"", "{", "null", "[]", "{}", `{"schema":0}`, `{"schema":-1}`, `{"schema":1.5}`, `{"schema":"1"}`, `{"schema":1} {}`, `{"schema":1} garbage`, `{"schema":1,"files":[]}`, `{"schema":1,"files":{"a":{"class":"other"}}}`} {
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "\nTry:") {
			t.Errorf("Parse(%q) = %v", data, err)
		}
	}
	if _, err := Parse([]byte(`{"schema":999}`)); err == nil || !strings.Contains(err.Error(), "Try: spinup self-update") {
		t.Fatalf("new schema error = %v", err)
	}
}

func TestNullMapsAndMarshalErrors(t *testing.T) {
	t.Parallel()
	l, err := Parse([]byte(`{"schema":1,"answers":null,"files":null,"stack_versions":null}`))
	if err != nil || l.Answers == nil || l.Files == nil || l.StackVersions == nil {
		t.Fatalf("Parse(null maps) = %#v, %v", l, err)
	}
	l.Answers["unsupported"] = make(chan int)
	if _, err := l.Marshal(); err == nil || !strings.Contains(err.Error(), "\nTry:") {
		t.Fatalf("Marshal(unsupported answer) = %v", err)
	}
	if err := l.Save(filepath.Join(t.TempDir(), "spinup.lock")); err == nil {
		t.Fatal("Save accepted an unsupported answer")
	}
	var absent *Lock
	if _, err := absent.Marshal(); err == nil {
		t.Fatal("Marshal accepted a nil lock")
	}
}

func TestMigrations(t *testing.T) {
	// This test changes the schema target before parallel tests start.
	previous := currentSchemaVersion
	currentSchemaVersion = 3
	t.Cleanup(func() {
		currentSchemaVersion = previous
		migrationMu.Lock()
		defer migrationMu.Unlock()
		delete(migrations, 1)
		delete(migrations, 2)
	})
	if _, err := Parse([]byte(`{"schema":1}`)); err == nil || !strings.Contains(err.Error(), "migration") {
		t.Fatalf("missing migration error = %v", err)
	}
	var order []int
	RegisterMigration(1, func(raw map[string]any) error {
		order = append(order, 1)
		raw["formatter_go"] = "go1.27.2"
		return nil
	})
	RegisterMigration(2, func(raw map[string]any) error {
		order = append(order, 2)
		if raw["formatter_go"] != "go1.27.2" || raw["schema"] != 2 {
			return errors.New("previous migration was not applied")
		}
		raw["behavior_version"] = 7
		return nil
	})
	l, err := Parse([]byte(`{"schema":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if l.Schema != 3 || l.FormatterGo != "go1.27.2" || l.BehaviorVersion != 7 || !reflect.DeepEqual(order, []int{1, 2}) {
		t.Fatalf("migrated lock = %#v, order = %v", l, order)
	}
	sentinel := errors.New("migration failed")
	RegisterMigration(1, func(map[string]any) error { return sentinel })
	if _, err := Parse([]byte(`{"schema":1}`)); !errors.Is(err, sentinel) {
		t.Fatalf("migration failure = %v", err)
	}
}

func TestSaveLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "spinup.lock")
	l := New()
	l.FormatterGo = "go1.27.2"
	for _, version := range []string{"v1", "v2"} {
		l.CLIVersion = version
		if err := l.Save(path); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || got.CLIVersion != version {
			t.Fatalf("Load() = %#v, %v", got, err)
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Schema = 0
	if err := l.Save(path); err == nil {
		t.Fatal("Save accepted an invalid lock")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed Save changed the file: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remain: %v, %v", entries, err)
	}
	if _, err := Load(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load missing file error = %v", err)
	}
	if err := New().Save(filepath.Join(dir, "missing", "spinup.lock")); err == nil {
		t.Fatal("Save succeeded without a directory")
	}
	if err := New().Save(dir); err == nil {
		t.Fatal("Save succeeded over a directory")
	}
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remain after failed rename: %v, %v", entries, err)
	}
}

func FuzzParse(f *testing.F) {
	for _, data := range []string{`{"schema":1}`, `{"schema":2}`, `{"schema":1,"answers":{"n":9007199254740993}}`, `{"schema":1,"files":{"a":{"class":"seed"}}}`, "null", "{"} {
		f.Add([]byte(data))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		l, err := Parse(data)
		if err != nil {
			return
		}
		first, err := l.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(first)
		if err != nil {
			t.Fatal(err)
		}
		second, err := parsed.Marshal()
		if err != nil || !bytes.Equal(first, second) {
			t.Fatalf("unstable roundtrip: %v", err)
		}
	})
}
