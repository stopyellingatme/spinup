package flags

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDeclaredValidate(t *testing.T) {
	many := Declared{}
	for i := range MaxFlags + 1 {
		many[fmt.Sprintf("f%d", i)] = Decl{}
	}
	tests := []struct {
		name string
		d    Declared
		want string
	}{
		{"empty", nil, ""},
		{"valid", Declared{"new_checkout": {Expires: "2027-03-31"}, "a": {}}, ""},
		{"too many", many, "65 flags are declared"},
		{"upper case", Declared{"New": {}}, `flags: the key "New" does not match ^[a-z][a-z0-9_]{0,47}$.` + "\nTry: correct [flags.New] in spinup.toml and run spinup g flags."},
		{"too long", Declared{strings.Repeat("a", 49): {}}, "does not match"},
		{"double underscore", Declared{"a__b": {}}, "double underscore"},
		{"trailing underscore", Declared{"ab_": {}}, "trailing underscore"},
		{"bad expires", Declared{"a": {Expires: "31.03.2027"}}, "not a YYYY-MM-DD date"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.d.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate = %v, want %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), "\nTry: ") {
				t.Fatalf("error has no Try: line: %v", err)
			}
		})
	}
}

func TestDeclaredKeys(t *testing.T) {
	got := Declared{"b": {}, "c": {}, "a": {}}.Keys()
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("Keys = %v", got)
	}
}

func TestDeclExpired(t *testing.T) {
	d := Decl{Expires: "2026-10-01"}
	day := func(s string) time.Time {
		v, _ := time.Parse(time.DateTime, s)
		return v
	}
	if d.Expired(day("2026-10-01 23:59:59")) {
		t.Fatal("expired on its own date")
	}
	if !d.Expired(day("2026-10-02 00:00:00")) {
		t.Fatal("not expired the day after")
	}
	if (Decl{}).Expired(day("2100-01-01 00:00:00")) {
		t.Fatal("empty Expires expired")
	}
	if (Decl{Expires: "bad"}).Expired(day("2100-01-01 00:00:00")) {
		t.Fatal("invalid Expires expired")
	}
}

func TestNewSnapshotCopies(t *testing.T) {
	declared := Declared{"a": {}}
	roles := []string{"staff", "manager", "staff"}
	users := map[int64]bool{1: true}
	rules := map[string]Rule{"a": {Roles: roles, Users: users}}
	at := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	s := NewSnapshot(declared, rules, at)

	declared["b"] = Decl{}
	roles[0] = "zzz"
	users[1] = false
	rules["c"] = Rule{}

	r, ok := s.Rule("a")
	if !ok || !slices.Equal(r.Roles, []string{"manager", "staff"}) || !r.Users[1] {
		t.Fatalf("Rule = %+v, %v", r, ok)
	}
	r.Roles[0] = "changed"
	r.Users[1] = false
	if again, _ := s.Rule("a"); again.Roles[0] != "manager" || !again.Users[1] {
		t.Fatal("Rule exposed the internal state")
	}
	if !slices.Equal(s.Keys(), []string{"a"}) {
		t.Fatalf("Keys = %v", s.Keys())
	}
	if !s.LoadedAt().Equal(at) {
		t.Fatalf("LoadedAt = %v", s.LoadedAt())
	}
	if got := Resolve(s, "b", Subject{}); got.Reason != ReasonError {
		t.Fatalf("later declaration reached the snapshot: %+v", got)
	}
	if _, ok := s.Rule("missing"); ok {
		t.Fatal("Rule of a missing key")
	}
	var nilSnap *Snapshot
	if len(nilSnap.Keys()) != 0 || !nilSnap.LoadedAt().IsZero() {
		t.Fatal("nil snapshot")
	}
}
