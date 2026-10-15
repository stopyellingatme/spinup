package deploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestHookRegistries(t *testing.T) {
	// The registries are package state. Do not run this test in parallel.
	beforePre, beforePost := preDeploy, postRelease
	preDeploy, postRelease = &hookRegistry{}, &hookRegistry{}
	t.Cleanup(func() { preDeploy, postRelease = beforePre, beforePost })
	var ran []string
	run := func(name string) func(context.Context, *HookContext) error {
		return func(context.Context, *HookContext) error { ran = append(ran, name); return nil }
	}
	for _, register := range []func(Hook){RegisterPreDeploy, RegisterPostRelease} {
		register(Hook{Name: "z", Run: run("z")})
		register(Hook{Name: "a", Run: run("a")})
		for _, bad := range []Hook{{Name: "z", Run: run("dup")}, {Run: run("empty")}, {Name: "nil"}} {
			func() {
				defer func() {
					if recover() == nil {
						t.Error("registration did not panic")
					}
				}()
				register(bad)
			}()
		}
	}
	for _, hooks := range [][]Hook{PreDeployHooks(), PostReleaseHooks()} {
		if err := RunHooks(context.Background(), hooks, &HookContext{}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(ran, []string{"a", "z", "a", "z"}) {
		t.Fatalf("order = %v", ran)
	}
	snapshot := PreDeployHooks()
	snapshot[0].Name = "changed"
	if PreDeployHooks()[0].Name != "a" {
		t.Fatal("registry slice was not copied")
	}
	sentinel := errors.New("broken")
	err := RunHooks(context.Background(), []Hook{{Name: "fail", Run: func(context.Context, *HookContext) error { return sentinel }}, {Name: "never", Run: run("never")}}, &HookContext{})
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "fail") || len(ran) != 4 {
		t.Fatalf("hook error = %v, ran = %v", err, ran)
	}
}

func TestRunHooksSortsAndPreservesInput(t *testing.T) {
	t.Parallel()
	var order []string
	c := &HookContext{App: "app"}
	makeHook := func(name string) Hook {
		return Hook{Name: name, Run: func(_ context.Context, got *HookContext) error {
			if got != c {
				t.Fatal("hook context changed")
			}
			order = append(order, name)
			return nil
		}}
	}
	hooks := []Hook{makeHook("z"), makeHook("a")}
	if err := RunHooks(context.Background(), hooks, c); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"a", "z"}) || hooks[0].Name != "z" {
		t.Fatalf("order = %v, input = %v", order, hooks)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunHooks(ctx, hooks, c); !errors.Is(err, context.Canceled) || len(order) != 2 {
		t.Fatalf("cancel = %v, order = %v", err, order)
	}
	if err := RunHooks(context.Background(), []Hook{{Name: "nil"}}, c); err == nil {
		t.Fatal("nil hook Run accepted")
	}
}
