package doctor

import (
	"context"
	"testing"
)

func TestRegistry(t *testing.T) {
	reset()
	t.Cleanup(reset)

	Register(Check{ID: "z", Scope: Local, Run: func(context.Context, *Context) Result { return Result{Status: OK} }})
	Register(Check{ID: "a", Scope: Local, Quick: true, Run: func(context.Context, *Context) Result { return Result{Status: OK} }})
	Register(Check{ID: "remote", Scope: Remote, Run: func(context.Context, *Context) Result { return Result{Status: OK} }})

	checks := Checks(Local, false)
	if len(checks) != 2 || checks[0].ID != "a" || checks[1].ID != "z" {
		t.Fatalf("Checks() = %#v", checks)
	}
	quick := Checks(Local, true)
	if len(quick) != 1 || quick[0].ID != "a" {
		t.Fatalf("quick Checks() = %#v", quick)
	}
}

func TestRegisterPanics(t *testing.T) {
	reset()
	t.Cleanup(reset)

	valid := Check{ID: "one", Run: func(context.Context, *Context) Result { return Result{} }}
	Register(valid)
	for _, test := range []struct {
		name  string
		check Check
	}{
		{name: "duplicate", check: valid},
		{name: "empty", check: Check{Run: valid.Run}},
		{name: "nil run", check: Check{ID: "nil"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("Register() did not panic")
				}
			}()
			Register(test.check)
		})
	}
}

func TestRunAllRecoversPanic(t *testing.T) {
	reset()
	t.Cleanup(reset)

	Register(Check{ID: "panic", Scope: Local, Run: func(context.Context, *Context) Result { panic("broken") }})
	Register(Check{ID: "ok", Scope: Local, Run: func(context.Context, *Context) Result { return Result{Status: OK} }})

	reports := RunAll(context.Background(), Local, &Context{})
	if len(reports) != 2 || reports[0].Check.ID != "ok" || reports[0].Result.Status != OK {
		t.Fatalf("RunAll() = %#v", reports)
	}
	if reports[1].Result.Status != Fail {
		t.Fatalf("panic status = %v, want Fail", reports[1].Result.Status)
	}
}
