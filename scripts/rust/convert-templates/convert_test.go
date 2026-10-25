package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConvert(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"trim", "a \n[[- if .ok -]]\nb[[- else ]] c [[ end ]]\n", "a[% if data.ok %]b[% else %] c [% endif %]\n"},
		{"pipeline", "[[ .value | snake | quote ]]", "[[ quote(snake(data.value)) ]]"},
		{"index", "[[ default \"x\" (index . \"missing\") ]]", "[[ go_default(\"x\", (get(data, \"missing\"))) ]]"},
		{"scope", "[[ with .user ]][[ .Name ]]/[[ $.app_name ]][[ end ]]", "[% set _with1 = data.user %][% if _with1 %][[ _with1.Name ]]/[[ data.app_name ]][% endif %]"},
		{"literal", "literal [% and [# and ]]", "literal [[ \"[%\" ]] and [[ \"[#\" ]] and ]]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := convert(tc.name, tc.source, "[[", "]]")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	boundary, _, err := convert("boundary", "Job[{{ .Name }}Args]", "{{", "}}")
	if err != nil {
		t.Fatal(err)
	}
	if boundary != "Job[[ \"[\" ]][[ data.Name ]]Args]" {
		t.Fatalf("boundary: %q", boundary)
	}

	if _, _, err := convert("unsupported", "[[ define \"x\" ]]x[[ end ]]", "[[", "]]"); err == nil {
		t.Fatal("accepted a named template")
	}
}

func TestConversionIsCurrent(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	out := t.TempDir() + "/packs"
	if err := convertRepository(root, out); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(out, func(name string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(out, name)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		want, err := os.ReadFile(filepath.Join(root, "spikes-rs/r0-02-minijinja/packs", rel))
		if err != nil {
			return err
		}
		if string(got) != string(want) {
			t.Errorf("stale conversion: %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	committed := filepath.Join(root, "spikes-rs/r0-02-minijinja/packs")
	err = filepath.WalkDir(committed, func(name string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(committed, name)
		if err != nil {
			return err
		}
		_, err = os.Stat(filepath.Join(out, rel))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

}
