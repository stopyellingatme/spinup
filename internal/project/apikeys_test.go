package project

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestAPIKeysDefaultsAndValidation(t *testing.T) {
	d := (&Config{}).APIKeysConfig()
	if d.Prefix != "key" || d.DefaultPlan != "free" || d.MaxKeys != 10 || d.Plans["free"] != (APIKeyPlan{60, 1000}) || d.Validate() != nil {
		t.Fatalf("defaults: %+v", d)
	}
	tests := []struct {
		name   string
		change func(*APIKeysDecl)
	}{
		{"prefix", func(d *APIKeysDecl) { d.Prefix = "X" }}, {"max low", func(d *APIKeysDecl) { d.MaxKeys = -1 }}, {"max high", func(d *APIKeysDecl) { d.MaxKeys = 101 }},
		{"no plans", func(d *APIKeysDecl) { d.Plans = nil }}, {"many plans", func(d *APIKeysDecl) {
			for i := 0; i < 17; i++ {
				d.Plans[fmt.Sprintf("p%d", i)] = APIKeyPlan{1, 1}
			}
		}},
		{"name", func(d *APIKeysDecl) { d.Plans["Bad"] = APIKeyPlan{1, 1} }}, {"minute low", func(d *APIKeysDecl) { d.Plans["free"] = APIKeyPlan{0, 1} }}, {"minute high", func(d *APIKeysDecl) { d.Plans["free"] = APIKeyPlan{100001, 100001} }},
		{"day low", func(d *APIKeysDecl) { d.Plans["free"] = APIKeyPlan{1, 0} }}, {"day high", func(d *APIKeysDecl) { d.Plans["free"] = APIKeyPlan{1, 100000001} }}, {"day less", func(d *APIKeysDecl) { d.Plans["free"] = APIKeyPlan{2, 1} }}, {"default", func(d *APIKeysDecl) { d.DefaultPlan = "missing" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := (&Config{}).APIKeysConfig()
			tc.change(&d)
			if d.Validate() == nil {
				t.Fatal("expected error")
			}
		})
	}
}
func TestAPIKeysParseAndRender(t *testing.T) {
	for _, text := range []string{"[apikeys]\nunknown=1", "[apikeys.plans.free]\nunknown=1"} {
		if _, err := Parse([]byte(text)); err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("%q: %v", text, err)
		}
	}
	a := (&Config{}).APIKeysConfig()
	a.Plans = map[string]APIKeyPlan{"z": {2, 3}, "free": {1, 2}}
	b := (&Config{}).APIKeysConfig()
	b.Plans = map[string]APIKeyPlan{}
	b.Plans["free"] = APIKeyPlan{1, 2}
	b.Plans["z"] = APIKeyPlan{2, 3}
	if !bytes.Equal(RenderAPIKeysFile(a), RenderAPIKeysFile(b)) {
		t.Fatal("render differs")
	}
	roles := (&Config{}).AuthRoles()
	roles.Admin = "owner"
	if !strings.Contains(string(RenderAPIKeysTSFile(a, roles)), "'owner'") {
		t.Fatal("missing admin role")
	}
}

func TestAPIKeysSeedBytes(t *testing.T) {
	var c Config
	d := c.APIKeysConfig()
	for path, want := range map[string][]byte{
		"../templates/packs/apikeys/files/" + APIKeysFilePath:   RenderAPIKeysFile(d),
		"../templates/packs/apikeys/files/" + APIKeysTSFilePath: RenderAPIKeysTSFile(d, c.AuthRoles()),
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s:\n%s\nwant:\n%s", path, got, want)
		}
	}
}
