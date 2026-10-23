package codemod

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestTOML(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("testdata/example.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, key string
		table     []string
		value     any
		comments  []string
	}{
		{"string", "name", []string{"app"}, "new", nil},
		{"array", "items", []string{"app"}, []string{"b", "c"}, []string{"# array", "# element"}},
		{"insert", "new", []string{"app"}, int64(2), nil},
		{"new table", "key", []string{"a.b", "space name"}, false, nil},
		{"root", "root", nil, false, nil},
		{"float", "float", nil, 1.25, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := SetTOML(src, tt.table, tt.key, tt.value)
			if err != nil {
				t.Fatal(err)
			}
			for _, comment := range []string{"# document", "# root", "# table", "# name", "# array", "# element", "# items", "# keep"} {
				if !bytes.Contains(out, []byte(comment)) {
					t.Fatalf("lost %s:\n%s", comment, out)
				}
			}
			again, err := SetTOML(out, tt.table, tt.key, tt.value)
			if err != nil || !bytes.Equal(out, again) {
				t.Fatalf("not idempotent: %v\n%s\n%s", err, out, again)
			}
			var doc map[string]any
			if err := toml.Unmarshal(out, &doc); err != nil {
				t.Fatal(err)
			}
		})
	}
	same, err := SetTOML(src, []string{"app"}, "name", "old")
	if err != nil || !bytes.Equal(src, same) {
		t.Fatal("no-op changed bytes", err)
	}
	same, err = SetTOML(src, []string{"app"}, "items", []string{"a"})
	if err != nil || !bytes.Equal(src, same) {
		t.Fatal("array no-op changed bytes", err)
	}
	for _, key := range []string{"name", "items", "missing"} {
		t.Run("delete "+key, func(t *testing.T) {
			t.Parallel()
			out, changed, err := DeleteTOML(src, []string{"app"}, key)
			if err != nil || changed != (key != "missing") {
				t.Fatal(changed, err)
			}
			if key == "missing" && !bytes.Equal(src, out) {
				t.Fatal("no-op changed bytes")
			}
			for _, comment := range []string{"# name", "# array", "# element", "# items"} {
				if !bytes.Contains(out, []byte(comment)) {
					t.Fatalf("lost %s:\n%s", comment, out)
				}
			}
			again, changed, err := DeleteTOML(out, []string{"app"}, key)
			if err != nil || changed || !bytes.Equal(out, again) {
				t.Fatal(changed, err)
			}
		})
	}
}

func TestTOMLErrors(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"a = [", "a=1\na=2"} {
		if _, err := SetTOML([]byte(src), nil, "a", "x"); err == nil {
			t.Fatal(src)
		}
		if _, _, err := DeleteTOML([]byte(src), nil, "a"); err == nil {
			t.Fatal(src)
		}
	}
	if _, err := SetTOML(nil, nil, "x", 42); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatal(err)
	}
}

func TestTOMLInsertKeepsFollowingTableComment(t *testing.T) {
	t.Parallel()
	src := []byte("[x]\nb = 2\n\n# about y\n[y]\nc = 3\n")
	out, err := SetTOML(src, []string{"x"}, "n", "v")
	if err != nil {
		t.Fatal(err)
	}
	want := "[x]\nb = 2\nn = 'v'\n\n# about y\n[y]\nc = 3\n"
	if string(out) != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
}

func TestTOMLNewTableUsesBareKeys(t *testing.T) {
	got, err := SetTOML([]byte("name = \"app\"\n"), []string{"env", "production", "digital ocean"}, "droplet_id", int64(7))
	if err != nil {
		t.Fatal(err)
	}
	want := "name = \"app\"\n[env.production.\"digital ocean\"]\ndroplet_id = 7\n"
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
