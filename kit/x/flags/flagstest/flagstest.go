package flagstest

import (
	"context"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/x/flags"
)

// Override forces key to value for every evaluation in this process until the test ends.
// The reason of an overridden evaluation is ReasonStatic. An undeclared key works.
//
// The override is global to the process. Do not call t.Parallel in a test
// that uses Override with a key that another test overrides.
func Override(t testing.TB, key string, value bool) {
	t.Helper()
	t.Cleanup(flags.SetOverride(key, value))
}

// Context returns ctx with a scope whose snapshot holds one global rule for each key,
// an anonymous subject, and a provider with the keys declared. For handler tests.
func Context(ctx context.Context, values map[string]bool) context.Context {
	return ContextAs(ctx, values, flags.Subject{Anonymous: true})
}

// ContextAs is Context with a signed-in subject.
//
// It panics when a key breaks the key rules of flags.Declared.Validate,
// because that is a fault in the test.
func ContextAs(ctx context.Context, values map[string]bool, s flags.Subject) context.Context {
	declared := make(flags.Declared, len(values))
	rules := make(map[string]flags.Rule, len(values))
	for key, on := range values {
		declared[key] = flags.Decl{Description: "test flag " + key}
		rules[key] = flags.Rule{Enabled: on}
	}
	f, err := flags.New(flags.WithDeclared(declared))
	if err != nil {
		panic("flagstest: " + err.Error())
	}
	f.Replace(flags.NewSnapshot(declared, rules, time.Time{}))
	return f.Context(ctx, s)
}
