package billing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/auth/authtest"
)

func TestRequire(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	free := f.user("free@example.com", "")
	pro := f.user("paid@example.com", "")
	f.subscribe(pro, "pro_monthly")
	f.drain()
	reads := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Has reuses the entitlement that Require read.
		ok, err := f.svc.Has(r.Context(), "export")
		if err != nil || !ok {
			t.Errorf("Has(export) = %t, %v", ok, err)
		}
		reads++
		w.WriteHeader(http.StatusNoContent)
	})
	h := f.svc.Require("basic", "export")(inner)
	serve := func(h http.Handler) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", "/x", nil))
		return w
	}
	if w := serve(h); w.Code != http.StatusUnauthorized {
		t.Fatalf("no user: %d", w.Code)
	}
	w := serve(authtest.As(h, free))
	expect(t, w, http.StatusPaymentRequired)
	out := decode(t, w)
	if out["type"] != "plan_required" || out["feature"] != "export" || out["status"] != float64(402) {
		t.Fatalf("problem = %v", out)
	}
	if detail, _ := out["detail"].(string); detail != `Your plan does not include the feature "export". Try: upgrade the plan on /pricing.` {
		t.Fatalf("detail = %q", detail)
	}
	if w := serve(authtest.As(h, pro)); w.Code != http.StatusNoContent || reads != 1 {
		t.Fatalf("pro: %d, %d reads", w.Code, reads)
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE billing_entitlements RENAME TO gone`); err != nil {
		t.Fatal(err)
	}
	expect(t, serve(authtest.As(h, pro)), http.StatusInternalServerError)
}

func TestHas(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	uid := f.user("has@example.com", "")
	ctx := auth.WithUser(t.Context(), auth.User{ID: uid})
	for feature, want := range map[string]bool{"basic": true, "export": false, "none": false} {
		if got, err := f.svc.Has(ctx, feature); err != nil || got != want {
			t.Errorf("Has(%s) = %t, %v", feature, got, err)
		}
	}
	if got, err := f.svc.Has(context.Background(), "basic"); err != nil || got {
		t.Errorf("Has without a user = %t, %v", got, err)
	}
	if _, err := f.svc.Entitlement(t.Context(), 99999); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Entitlement of a missing user: %v", err)
	}
	// A stored plan that is no longer declared falls back to the default plan.
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO billing_entitlements(user_id,plan,features,status) VALUES($1,'gold','{gold}','active')`, uid); err != nil {
		t.Fatal(err)
	}
	if e := f.entitlement(uid); e.Plan != "free" || e.Status != "" || len(e.Features) != 1 {
		t.Fatalf("undeclared plan = %+v", e)
	}
}
