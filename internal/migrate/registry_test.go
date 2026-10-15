package migrate

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func migration(id, since string) Migration {
	return Migration{ID: id, Since: since, Stage: After, Plan: func(*Ctx) ([]Change, error) { return []Change{{Path: id}}, nil }, Apply: func(*Ctx) error { return nil }}
}

func TestRegistry(t *testing.T) {
	t.Parallel()
	var r Registry
	for _, m := range []Migration{migration("release", "0.2.0"), migration("b", "0.2.0-rc.10"), migration("a", "0.2.0-rc.2"), migration("c", "0.2.0-rc.2")} {
		r.Register(m)
	}
	ids := func(ms []Migration) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	if got := ids(r.All()); !reflect.DeepEqual(got, []string{"a", "c", "b", "release"}) {
		t.Fatal(got)
	}
	all := r.All()
	all[0].ID = "changed"
	if r.All()[0].ID != "a" {
		t.Fatal("All aliases registry")
	}
	for _, tt := range []struct {
		from, to string
		applied  []string
		stage    Stage
		want     []string
		bad      bool
	}{
		{"0.1.0", "0.2.0", nil, After, []string{"a", "c", "b", "release"}, false},
		{"0.2.0-rc.2", "0.2.0", []string{"b"}, After, []string{"release"}, false},
		{"0.1.0", "0.2.0-rc.2", nil, After, []string{"a", "c"}, false},
		{"0.1.0", "0.2.0", nil, Before, nil, false},
		{"bad", "0.2.0", nil, After, nil, true}, {"0.1.0", "bad", nil, After, nil, true},
		{"0.2.0", "0.1.0", nil, After, nil, true},
	} {
		t.Run(tt.from+tt.to, func(t *testing.T) {
			ms, err := r.Pending(tt.from, tt.to, tt.applied, tt.stage)
			if (err != nil) != tt.bad {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ids(ms), tt.want) {
				t.Fatal(ids(ms), tt.want)
			}
		})
	}
}

func TestRegisterPanics(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"empty", "duplicate", "since", "stage", "plan", "apply"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var r Registry
			m := migration("test", "0.2.0")
			switch name {
			case "empty":
				m.ID = ""
			case "duplicate":
				r.Register(m)
			case "since":
				m.Since = "no"
			case "stage":
				m.Stage = 0
			case "plan":
				m.Plan = nil
			case "apply":
				m.Apply = nil
			}
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			r.Register(m)
		})
	}
}

func TestRunAndPlan(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("broken input")
	for _, phase := range []string{"plan", "apply"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			ms := []Migration{migration("first", "0.2.0"), migration("bad", "0.2.0"), migration("last", "0.2.0")}
			ms[1].Plan = func(*Ctx) ([]Change, error) { return nil, sentinel }
			ms[1].Apply = func(*Ctx) error { return sentinel }
			ms[2].Apply = func(*Ctx) error { t.Fatal("ran past failure"); return nil }
			var err error
			if phase == "plan" {
				var changes []Change
				changes, err = PlanAll(&Ctx{}, ms)
				if len(changes) != 1 || changes[0].Path != "first" {
					t.Fatal(changes)
				}
			} else {
				var applied []string
				applied, err = Run(&Ctx{}, ms)
				if !reflect.DeepEqual(applied, []string{"first"}) {
					t.Fatal(applied)
				}
			}
			if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "bad") || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatal(err)
			}
		})
	}
	ms := []Migration{migration("ok", "0.2.0")}
	if ids, err := Run(&Ctx{}, ms); err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	if changes, err := PlanAll(&Ctx{}, ms); err != nil || len(changes) != 1 {
		t.Fatal(changes, err)
	}
}

func TestKitNotes(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, data string
		valid      bool
	}{
		{"valid", `{"schema":1,"version":"0.2.0","steps":[{"id":"a","desc":"rename","kind":"codemod","migration":"a"},{"kind":"fix-inline"},{"kind":"manual"}]}`, true},
		{"schema", `{"schema":2}`, false}, {"unknown", `{"schema":1,"extra":true}`, false},
		{"step unknown", `{"schema":1,"steps":[{"kind":"manual","extra":1}]}`, false},
		{"kind", `{"schema":1,"steps":[{"kind":"other"}]}`, false},
		{"migration", `{"schema":1,"steps":[{"kind":"codemod"}]}`, false},
		{"trailing", `{"schema":1} {}`, false}, {"null", `null`, false}, {"syntax", `{`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			notes, err := ParseKitNotes([]byte(tt.data))
			if (err == nil) != tt.valid {
				t.Fatal(notes, err)
			}
		})
	}
}
