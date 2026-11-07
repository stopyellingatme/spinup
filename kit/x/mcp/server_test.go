package mcp

import (
	"context"
	"strings"
	"testing"
)

func TestNewValidation(t *testing.T) {
	for _, cfg := range []Config{{}, {Name: "app"}, {Version: "v1"}, {Name: "app", Version: "v1", MaxBodyBytes: -1}, {Name: "app", Version: "v1", MaxResultBytes: -1}} {
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "\nTry: ") {
			t.Errorf("New(%+v): %v", cfg, err)
		}
	}
	s, err := New(Config{Name: "app", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if s.cfg.MaxBodyBytes != 1<<20 || s.cfg.MaxResultBytes != 256<<10 {
		t.Fatalf("defaults: %+v", s.cfg)
	}
	if s.SDK() == nil {
		t.Fatal("missing SDK server")
	}
}

func TestToolValidation(t *testing.T) {
	for _, def := range []ToolDef{{Name: "bad-name", Description: "a"}, {Name: "Upper", Description: "a"}, {Name: strings.Repeat("a", 65), Description: "a"}, {Name: "", Description: "a"}, {Name: "a"}, {Name: "a", Description: "a", ReadOnly: true, Destructive: true}} {
		t.Run(def.Name, func(t *testing.T) {
			s, err := New(Config{Name: "app", Version: "v1"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if recover() == nil {
					t.Error("Tool did not panic")
				}
			}()
			Tool(s, def, func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
		})
	}
	s, err := New(Config{Name: "app", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	def := ToolDef{Name: "valid_2", Description: "a", Roles: []string{"staff"}}
	h := func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil }
	Tool(s, def, h)
	def.Roles[0] = "changed"
	defs := s.Tools()
	defs[0].Roles[0] = "changed again"
	if s.Tools()[0].Roles[0] != "staff" {
		t.Fatal("tool roles were not copied")
	}
	defer func() {
		if recover() == nil {
			t.Error("duplicate Tool did not panic")
		}
	}()
	Tool(s, def, h)
}
