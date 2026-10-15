package modules

import (
	"slices"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func TestCatalog(t *testing.T) {
	t.Parallel()
	names, err := templates.List(templates.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, name := range names {
		if name == "base" || name == "api" || name == "web" || name[0] == '_' {
			continue
		}
		want = append(want, name)
		got, ok := Get(name)
		if !ok || got.Description == "" {
			t.Fatalf("missing metadata: %s", name)
		}
	}
	var got []string
	for _, m := range All() {
		got = append(got, m.Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	auth, _ := Get("auth")
	if !auth.NeedsWeb {
		t.Fatal("auth must require web")
	}
	auth.Requires[0] = "changed"
	auth, _ = Get("auth")
	if !slices.Contains(auth.Requires, "web") {
		t.Fatal("catalog is mutable")
	}
}

func TestEnabled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  *project.Config
		want bool
	}{
		{"jobs", &project.Config{Modules: []string{"jobs"}}, true},
		{"jobs", &project.Config{Modules: []string{"ci"}}, false},
		{"jobs", nil, false},
	} {
		if got := Enabled(tc.cfg, tc.name); got != tc.want {
			t.Fatalf("Enabled(%s) = %v", tc.name, got)
		}
	}
	if WebEnabled(nil) || WebEnabled(&manifest.Lock{Packs: []string{"base", "api"}}) || !WebEnabled(&manifest.Lock{Packs: []string{"web"}}) {
		t.Fatal("wrong web detection")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("unknown module did not panic")
		}
	}()
	Enabled(nil, "bogus")
}
