package registry

import (
	"context"
	"reflect"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
)

func reset() { mu.Lock(); defer mu.Unlock(); generators = nil }

func TestRegistry(t *testing.T) {
	reset()
	t.Cleanup(reset)
	run := func(context.Context, gen.Input) (*gen.Output, error) { return &gen.Output{}, nil }
	Register(gen.Generator{Name: "zebra", Aliases: []string{"z"}, Run: run})
	Register(gen.Generator{Name: "alpha", Run: run})
	if got := All(); len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zebra" {
		t.Fatalf("All() = %v", got)
	}
	for _, name := range []string{"z", "zebra"} {
		if got, ok := Lookup(name); !ok || got.Name != "zebra" {
			t.Fatalf("Lookup(%q) = %v, %v", name, got, ok)
		}
	}
	if _, ok := Lookup("missing"); ok {
		t.Fatal("found missing generator")
	}
	all := All()
	all[1].Aliases[0] = "changed"
	got, _ := Lookup("zebra")
	if !reflect.DeepEqual(got.Aliases, []string{"z"}) {
		t.Fatal("All leaked aliases")
	}
	for _, g := range []gen.Generator{
		{Name: "", Run: run}, {Name: "missing-run"}, {Name: "alpha", Run: run},
		{Name: "z", Run: run}, {Name: "beta", Aliases: []string{"alpha"}, Run: run},
		{Name: "beta", Aliases: []string{""}, Run: run}, {Name: "beta", Aliases: []string{"beta"}, Run: run},
		{Name: "beta", Aliases: []string{"b", "b"}, Run: run},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Register(%+v) did not panic", g)
				}
			}()
			Register(g)
		}()
	}
	if len(All()) != 2 {
		t.Fatal("failed registration modified registry")
	}
}
