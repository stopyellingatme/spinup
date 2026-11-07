package flagstest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stopyellingatme/spinup/kit/x/flags"
	"github.com/stopyellingatme/spinup/kit/x/flags/flagstest"
)

func TestOverrideRestoresAtCleanup(t *testing.T) {
	ctx := flagstest.Context(context.Background(), map[string]bool{"new_checkout": false})
	t.Run("inner", func(t *testing.T) {
		flagstest.Override(t, "new_checkout", true)
		flagstest.Override(t, "not_declared", true)
		if got := flags.Evaluate(ctx, "new_checkout"); got != (flags.Resolution{Value: true, Variant: "on", Reason: flags.ReasonStatic}) {
			t.Fatalf("Evaluate = %+v", got)
		}
		if !flags.Enabled(context.Background(), "not_declared") {
			t.Fatal("an undeclared override is off")
		}
	})
	if flags.Enabled(ctx, "new_checkout") {
		t.Fatal("the override survived cleanup")
	}
	if flags.Enabled(context.Background(), "not_declared") {
		t.Fatal("the undeclared override survived cleanup")
	}
}

func TestContextDrivesHandler(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if flags.Enabled(r.Context(), "new_checkout") {
			_, _ = w.Write([]byte("new"))
			return
		}
		_, _ = w.Write([]byte("old"))
	})
	for _, on := range []bool{true, false} {
		req := httptest.NewRequestWithContext(flagstest.Context(context.Background(), map[string]bool{"new_checkout": on}), http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		want := "old"
		if on {
			want = "new"
		}
		if rec.Body.String() != want {
			t.Fatalf("on=%v: body %q", on, rec.Body.String())
		}
	}
	s, ok := flags.SubjectFrom(flagstest.Context(context.Background(), nil))
	if !ok || !s.Anonymous {
		t.Fatalf("Context subject = %+v, %v", s, ok)
	}
}

func TestContextAs(t *testing.T) {
	subject := flags.Subject{UserID: 5, Role: "manager"}
	ctx := flagstest.ContextAs(context.Background(), map[string]bool{"beta": true}, subject)
	if s, _ := flags.SubjectFrom(ctx); s != subject {
		t.Fatalf("subject = %+v", s)
	}
	if got := flags.Evaluate(ctx, "beta"); got.Reason != flags.ReasonStatic || !got.Value {
		t.Fatalf("Evaluate = %+v", got)
	}
	if got := flags.Evaluate(ctx, "other"); got.Reason != flags.ReasonError {
		t.Fatalf("undeclared = %+v", got)
	}
}
