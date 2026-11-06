package flags

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func newTestFlags(t testing.TB) *Flags {
	t.Helper()
	f, err := New(WithDeclared(testDeclared))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestNew(t *testing.T) {
	_, err := New(WithDeclared(Declared{"New": {}}))
	if err == nil || !strings.HasSuffix(err.Error(), "\nTry: correct [flags.New] in spinup.toml and run spinup g flags.") {
		t.Fatalf("New = %v", err)
	}
	f, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if f.Metadata().Name != "spinup" {
		t.Fatalf("Metadata = %+v", f.Metadata())
	}
	if len(f.Declared()) != 0 || f.Logger() == nil {
		t.Fatal("empty provider")
	}
	if got := f.Resolve("new_checkout", Subject{}); got.Reason != ReasonError {
		t.Fatalf("Resolve without declarations = %+v", got)
	}
}

func TestFlagsDeclaredIsCopy(t *testing.T) {
	f := newTestFlags(t)
	d := f.Declared()
	d["extra"] = Decl{}
	delete(d, "dark_mode")
	if _, ok := f.Declared()["dark_mode"]; !ok {
		t.Fatal("Declared returned the internal map")
	}
}

func TestFlagsSnapshotAndReplace(t *testing.T) {
	f := newTestFlags(t)
	s := f.Snapshot()
	if s == nil || !s.LoadedAt().IsZero() || len(s.Keys()) != 0 {
		t.Fatalf("initial snapshot = %+v", s)
	}
	if got := f.Resolve("dark_mode", Subject{UserID: 1}); got != (Resolution{true, "on", ReasonDefault}) {
		t.Fatalf("Resolve before load = %+v", got)
	}
	f.Replace(NewSnapshot(f.Declared(), map[string]Rule{"dark_mode": {}}, time.Unix(1, 0)))
	if got := f.Resolve("dark_mode", Subject{UserID: 1}); got != (Resolution{false, "off", ReasonStatic}) {
		t.Fatalf("Resolve after load = %+v", got)
	}
	f.Replace(nil)
	if !f.Snapshot().LoadedAt().IsZero() {
		t.Fatal("Replace(nil) did not reset")
	}
}

func TestBooleanEvaluation(t *testing.T) {
	f := newTestFlags(t)
	var p Provider = f
	f.Replace(NewSnapshot(f.Declared(), map[string]Rule{"new_checkout": {Enabled: true}}, time.Time{}))
	ctx := context.Background()
	if got := p.BooleanEvaluation(ctx, "missing", true, Subject{}); got != (Resolution{true, "on", ReasonError}) {
		t.Fatalf("undeclared = %+v", got)
	}
	if got := p.BooleanEvaluation(ctx, "missing", false, Subject{}); got != (Resolution{false, "off", ReasonError}) {
		t.Fatalf("undeclared = %+v", got)
	}
	if got := p.BooleanEvaluation(ctx, "new_checkout", false, Subject{Anonymous: true}); got != (Resolution{true, "on", ReasonStatic}) {
		t.Fatalf("declared = %+v", got)
	}
}

func TestScopeKeepsOneSnapshot(t *testing.T) {
	f := newTestFlags(t)
	f.Replace(NewSnapshot(f.Declared(), map[string]Rule{"new_checkout": {Enabled: true}}, time.Time{}))
	ctx := f.Context(context.Background(), Subject{UserID: 3, Role: "staff"})
	f.Replace(NewSnapshot(f.Declared(), map[string]Rule{"new_checkout": {}}, time.Time{}))
	if !Enabled(ctx, "new_checkout") {
		t.Fatal("Replace changed the answer of an installed scope")
	}
	if Enabled(f.Context(context.Background(), Subject{UserID: 3}), "new_checkout") {
		t.Fatal("a new scope did not see the new snapshot")
	}
	s, ok := SubjectFrom(ctx)
	if !ok || s != (Subject{UserID: 3, Role: "staff"}) {
		t.Fatalf("SubjectFrom = %+v, %v", s, ok)
	}
	// The helper for the middleware falls back to the empty snapshot.
	ctx = f.scope(context.Background(), nil, Subject{Anonymous: true})
	if got := Evaluate(ctx, "dark_mode"); got != (Resolution{true, "on", ReasonDefault}) {
		t.Fatalf("scope(nil) = %+v", got)
	}
}

func TestNoScope(t *testing.T) {
	ctx := context.Background()
	if got := Evaluate(ctx, "new_checkout"); got != (Resolution{false, "off", ReasonError}) {
		t.Fatalf("Evaluate without scope = %+v", got)
	}
	if Enabled(ctx, "new_checkout") {
		t.Fatal("Enabled without scope")
	}
	if _, ok := SubjectFrom(ctx); ok {
		t.Fatal("SubjectFrom without scope")
	}
}

func TestEvaluateUndeclaredInScope(t *testing.T) {
	f := newTestFlags(t)
	ctx := f.Context(context.Background(), Subject{UserID: 1})
	if got := Evaluate(ctx, "missing"); got != (Resolution{false, "off", ReasonError}) {
		t.Fatalf("Evaluate undeclared = %+v", got)
	}
}

func TestOverride(t *testing.T) {
	f := newTestFlags(t)
	f.Replace(NewSnapshot(f.Declared(), map[string]Rule{"new_checkout": {}}, time.Time{}))
	ctx := f.Context(context.Background(), Subject{UserID: 1})

	restore := SetOverride("new_checkout", true)
	inner := SetOverride("new_checkout", false)
	if got := Evaluate(ctx, "new_checkout"); got != (Resolution{false, "off", ReasonStatic}) {
		t.Fatalf("inner override = %+v", got)
	}
	inner()
	inner() // a second call does nothing
	want := Resolution{true, "on", ReasonStatic}
	for name, got := range map[string]Resolution{
		"Evaluate":          Evaluate(ctx, "new_checkout"),
		"Resolve":           Resolve(f.Snapshot(), "new_checkout", Subject{}),
		"Flags.Resolve":     f.Resolve("new_checkout", Subject{}),
		"BooleanEvaluation": f.BooleanEvaluation(ctx, "new_checkout", false, Subject{}),
		"no scope":          Evaluate(context.Background(), "new_checkout"),
	} {
		if got != want {
			t.Fatalf("%s = %+v, want %+v", name, got, want)
		}
	}
	undeclared := SetOverride("not_declared", true)
	if got := f.BooleanEvaluation(ctx, "not_declared", false, Subject{}); got != want {
		t.Fatalf("undeclared override = %+v", got)
	}
	undeclared()
	restore()
	if overrideActive.Load() {
		t.Fatal("fast path still set after the last restore")
	}
	if Enabled(ctx, "new_checkout") {
		t.Fatal("override survived restore")
	}
}

func TestSpanEvent(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	f := newTestFlags(t)
	f.Replace(NewSnapshot(f.Declared(), map[string]Rule{"new_checkout": {Users: map[int64]bool{99: true}}}, time.Time{}))
	ctx, span := tp.Tracer("test").Start(context.Background(), "request")
	ctx = f.Context(ctx, Subject{UserID: 99})
	_ = Enabled(ctx, "new_checkout")
	_ = f.BooleanEvaluation(ctx, "missing", true, Subject{})
	_ = f.Resolve("new_checkout", Subject{UserID: 99}) // pure: no event
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	events := spans[0].Events()
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	type want struct {
		key, variant, reason string
		value                bool
	}
	for i, w := range []want{
		{"new_checkout", "on", "targeting_match", true},
		{"missing", "on", "error", true},
	} {
		ev := events[i]
		if ev.Name != "feature_flag.evaluation" {
			t.Fatalf("event name = %q", ev.Name)
		}
		got := map[string]string{}
		for _, a := range ev.Attributes {
			got[string(a.Key)] = a.Value.String()
		}
		exp := map[string]string{
			"feature_flag.key":            w.key,
			"feature_flag.provider.name":  "spinup",
			"feature_flag.result.value":   fmt.Sprint(w.value),
			"feature_flag.result.variant": w.variant,
			"feature_flag.result.reason":  w.reason,
		}
		if len(got) != len(exp) {
			t.Fatalf("event %d attributes = %v", i, got)
		}
		for k, v := range exp {
			if got[k] != v {
				t.Fatalf("event %d %s = %q, want %q", i, k, got[k], v)
			}
		}
	}
}

func BenchmarkEnabled(b *testing.B) {
	f := newTestFlags(b)
	f.Replace(NewSnapshot(f.Declared(), map[string]Rule{"new_checkout": {Percentage: 50, Roles: []string{"manager"}}}, time.Time{}))
	ctx := f.Context(context.Background(), Subject{UserID: 4242, Role: "staff"})
	b.Run("no override", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = Enabled(ctx, "new_checkout")
		}
	})
	b.Run("override table active", func(b *testing.B) {
		restore := SetOverride("other_flag", true)
		defer restore()
		b.ReportAllocs()
		for b.Loop() {
			_ = Enabled(ctx, "new_checkout")
		}
	})
}

func TestEnabledZeroAllocs(t *testing.T) {
	f := newTestFlags(t)
	f.Replace(NewSnapshot(f.Declared(), map[string]Rule{"new_checkout": {Percentage: 50, Roles: []string{"manager"}}}, time.Time{}))
	ctx := f.Context(context.Background(), Subject{UserID: 4242, Role: "staff"})
	if n := testing.AllocsPerRun(1000, func() { _ = Enabled(ctx, "new_checkout") }); n != 0 {
		t.Fatalf("Enabled allocates %v times per call", n)
	}
}
