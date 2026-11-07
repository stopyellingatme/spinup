package apikeys

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/auth"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func testStore(t *testing.T, now *time.Time) *Store {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), "postgres://postgres:postgres@127.0.0.1:55433/postgres?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s, err := NewStore(pool, testConfig(), WithClock(func() time.Time { return *now }), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func requestContext(ctx context.Context, req Request) context.Context {
	return context.WithValue(ctx, requestKey{}, req)
}
func testRequest() Request {
	return Request{Key: Key{ID: 7, UserID: 1, Scope: ScopeRead}, PlanName: "free", Plan: Plan{PerMinute: 60, PerDay: 1000}}
}

func TestMiddlewareMatrix(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		key          bool
		scope        Scope
		status       int
		detail       string
	}{
		{"POST", "/api/admin/users", false, ScopeRead, 204, ""},
		{"GET", "/api/me/keys", true, ScopeWrite, 403, "An API key cannot manage keys or call admin routes."},
		{"HEAD", "/api/me/keys/7", true, ScopeWrite, 403, "An API key cannot manage keys or call admin routes."},
		{"GET", "/api/admin/users", true, ScopeWrite, 403, "An API key cannot manage keys or call admin routes."},
		{"POST", "/api/notes", true, ScopeRead, 403, "This API key is read-only."},
		{"PUT", "/api/notes", true, ScopeRead, 403, "This API key is read-only."},
		{"PATCH", "/api/notes", true, ScopeRead, 403, "This API key is read-only."},
		{"DELETE", "/api/notes", true, ScopeRead, 403, "This API key is read-only."},
		{"OPTIONS", "/api/notes", true, ScopeRead, 403, "This API key is read-only."},
		{"CONNECT", "/api/notes", true, ScopeRead, 403, "This API key is read-only."},
		{"TRACE", "/api/notes", true, ScopeRead, 403, "This API key is read-only."},
		{"GET", "/api/notes", true, ScopeRead, 204, ""},
		{"HEAD", "/api/notes", true, ScopeRead, 204, ""},
		{"POST", "/api/notes", true, ScopeWrite, 204, ""},
	} {
		t.Run(tc.method+tc.path+string(tc.scope), func(t *testing.T) {
			now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
			s := testStore(t, &now)
			s.days[userDay{1, utcDay(now)}] = &dayUsage{base: 42}
			r := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			ctx := audit.WithActor(r.Context(), audit.Actor{ID: 1, Email: "u@example.com", Reason: "before"})
			req := testRequest()
			req.Key.Scope = tc.scope
			if tc.key {
				ctx = requestContext(ctx, req)
			}
			r = r.WithContext(ctx)
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				actor, _ := audit.ActorFrom(r.Context())
				expected := "before"
				if tc.key {
					expected = "api_key:7"
				}
				if actor.Reason != expected || actor.Email != "u@example.com" {
					t.Fatalf("actor = %+v", actor)
				}
				w.WriteHeader(http.StatusNoContent)
			})
			w := httptest.NewRecorder()
			s.Middleware()(next).ServeHTTP(w, r)
			if w.Code != tc.status || called != (tc.status == 204) {
				t.Fatalf("status=%d called=%v body=%s", w.Code, called, w.Body)
			}
			if !strings.Contains(w.Body.String(), tc.detail) {
				t.Fatalf("body = %s", w.Body)
			}
			if tc.key {
				remaining := "958"
				if tc.status == 204 {
					remaining = "957"
				}
				if w.Header().Get("RateLimit-Limit") != "1000" || w.Header().Get("RateLimit-Remaining") != remaining || w.Header().Get("RateLimit-Reset") != "43200" {
					t.Fatalf("headers = %v", w.Header())
				}
				if tc.status == 403 && w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("problem is cacheable")
				}
			} else if len(w.Header()) != 0 {
				t.Fatalf("cookie headers = %v", w.Header())
			}
		})
	}
}

func TestMiddlewareQuotaLimits(t *testing.T) {
	now := time.Date(2026, 11, 6, 23, 58, 59, 0, time.UTC)
	s := testStore(t, &now)
	who := userDay{1, utcDay(now)}
	s.days[who] = &dayUsage{}
	req := testRequest()
	req.Plan = Plan{PerMinute: 2, PerDay: 3}
	h := s.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	call := func(keyID int64) *httptest.ResponseRecorder {
		req.Key.ID = keyID
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/notes", nil)
		r = r.WithContext(requestContext(r.Context(), req))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, id := range []int64{7, 8} {
		if w := call(id); w.Code != 204 {
			t.Fatal(w.Body)
		}
	}
	w := call(7)
	if w.Code != 429 || w.Header().Get("Retry-After") != "30" || !strings.Contains(w.Body.String(), "The free plan allows 2 requests a minute. Try again in 30 seconds.") {
		t.Fatalf("minute: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w.Header().Get("RateLimit-Remaining") != "1" {
		t.Fatal(w.Header())
	}
	now = now.Add(30 * time.Second)
	if w = call(8); w.Code != 204 {
		t.Fatal(w.Body)
	}
	now = now.Add(30 * time.Second)
	w = call(7)
	if w.Code != 429 || w.Header().Get("Retry-After") != "1" || w.Header().Get("RateLimit-Remaining") != "0" || !strings.Contains(w.Body.String(), "The free plan allows 3 requests a day. Try again after midnight UTC.") {
		t.Fatalf("day: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if s.pending[keyDay{7, 1, who.day}] != 1 || s.pending[keyDay{8, 1, who.day}] != 2 {
		t.Fatalf("pending = %v", s.pending)
	}
}

func TestMiddlewareConcurrentDayLimit(t *testing.T) {
	now := time.Now()
	s := testStore(t, &now)
	req := testRequest()
	req.Plan = Plan{PerMinute: 1000, PerDay: 5}
	s.days[userDay{1, utcDay(now)}] = &dayUsage{base: 2}
	h := s.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	var wg sync.WaitGroup
	results := make(chan int, 50)
	for range 50 {
		wg.Go(func() {
			r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/notes", nil)
			r = r.WithContext(requestContext(r.Context(), req))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			results <- w.Code
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == 204 {
			success++
		} else if status != 429 {
			t.Fatal(status)
		}
	}
	if success != 3 {
		t.Fatalf("accepted %d requests", success)
	}
}

func TestWrongTokenWithoutDatabase(t *testing.T) {
	now := time.Now()
	s := testStore(t, &now)
	s.pool.Close()
	for _, token := range []string{"", "wrong_" + strings.Repeat("x", 43), "key_" + strings.Repeat("x", 42), "key_" + strings.Repeat("x", 44)} {
		_, _, err := s.AuthenticateBearer(t.Context(), token)
		if !errors.Is(err, auth.ErrBearerInvalid) {
			t.Fatalf("wrong token: %v", err)
		}
	}
}

func TestMiddlewareDatabaseFailure(t *testing.T) {
	now := time.Now()
	s := testStore(t, &now)
	s.pool.Close()
	r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/notes", nil)
	r = r.WithContext(requestContext(r.Context(), testRequest()))
	w := httptest.NewRecorder()
	s.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("called next") })).ServeHTTP(w, r)
	if w.Code != 503 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("RateLimit-Limit") != "1000" {
		t.Fatalf("response = %d %v %s", w.Code, w.Header(), w.Body)
	}
}

func TestMiddlewareWithoutActorAndRoundedRetry(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	req := testRequest()
	req.Plan = Plan{PerMinute: 2, PerDay: 1000}
	s.days[userDay{1, utcDay(now)}] = &dayUsage{}
	calls := 0
	h := s.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if _, ok := audit.ActorFrom(r.Context()); ok {
			t.Fatal("middleware invented an audit actor")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(requestContext(t.Context(), req), "GET", "/api/notes", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	call()
	call()
	now = now.Add(500 * time.Millisecond)
	w := call()
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "30" || calls != 2 {
		t.Fatalf("rounded retry: %d %v calls=%d", w.Code, w.Header(), calls)
	}
}

func TestMiddlewareRPCPath(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, rpc string
		status                  int
	}{
		{"post on the path", "POST", "/api/mcp", "/api/mcp", 204},
		{"get on the path", "GET", "/api/mcp", "/api/mcp", 204},
		{"put on the path", "PUT", "/api/mcp", "/api/mcp", 403},
		{"delete on the path", "DELETE", "/api/mcp", "/api/mcp", 403},
		{"longer path", "POST", "/api/mcp/x", "/api/mcp", 403},
		{"other path", "POST", "/api/notes", "/api/mcp", 403},
		{"empty option", "POST", "/api/mcp", "", 403},
		{"keys path first", "POST", "/api/me/keys", "/api/me/keys", 403},
		{"admin path first", "POST", "/api/admin/x", "/api/admin/x", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
			s := testStore(t, &now)
			s.rpcPath = tc.rpc
			s.days[userDay{1, utcDay(now)}] = &dayUsage{base: 42}
			r := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			r = r.WithContext(requestContext(r.Context(), testRequest()))
			w := httptest.NewRecorder()
			s.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, tc.status, w.Body)
			}
		})
	}
}

func TestWithRPCPathSetsStorePath(t *testing.T) {
	now := time.Now()
	s := testStore(t, &now)
	if s.rpcPath != "" {
		t.Fatal("default path is not empty")
	}
	var o storeOptions
	WithRPCPath("/api/mcp")(&o)
	if o.rpc != "/api/mcp" {
		t.Fatalf("rpc = %q", o.rpc)
	}
}

func TestMiddlewareRPCPathCountsQuota(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	s.rpcPath = "/api/mcp"
	s.days[userDay{1, utcDay(now)}] = &dayUsage{}
	req := testRequest()
	req.Plan = Plan{PerMinute: 1, PerDay: 10}
	h := s.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/api/mcp", nil)
		r = r.WithContext(requestContext(r.Context(), req))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call(); w.Code != 204 || w.Header().Get("RateLimit-Remaining") != "9" {
		t.Fatalf("first: %d %v", w.Code, w.Header())
	}
	if w := call(); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second: %d", w.Code)
	}
}

func TestWithSessionOnlyOption(t *testing.T) {
	var o storeOptions
	WithSessionOnly("/api/billing/", "")(&o)
	WithSessionOnly("/api/other")(&o)
	if len(o.only) != 2 || o.only[0] != "/api/billing/" || o.only[1] != "/api/other" {
		t.Fatalf("only = %v", o.only)
	}
}

func TestMiddlewareSessionOnly(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		key                bool
		prefixes           []string
		status             int
	}{
		{"key on prefix", "GET", "/api/billing/checkout", true, []string{"/api/billing/"}, 403},
		{"key on prefix post", "POST", "/api/billing/portal", true, []string{"/api/billing/"}, 403},
		{"no key on prefix", "GET", "/api/billing/checkout", false, []string{"/api/billing/"}, 204},
		{"key off prefix", "GET", "/api/notes", true, []string{"/api/billing/"}, 204},
		{"second prefix", "GET", "/api/other/x", true, []string{"/api/billing/", "/api/other"}, 403},
		{"empty prefix ignored", "GET", "/api/notes", true, []string{""}, 204},
		{"no prefixes", "GET", "/api/billing/checkout", true, nil, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
			s := testStore(t, &now)
			var o storeOptions
			WithSessionOnly(tc.prefixes...)(&o)
			s.sessionOnly = o.only
			s.days[userDay{1, utcDay(now)}] = &dayUsage{}
			r := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			if tc.key {
				r = r.WithContext(requestContext(r.Context(), testRequest()))
			}
			w := httptest.NewRecorder()
			s.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, tc.status, w.Body)
			}
			if tc.status == 403 && !strings.Contains(w.Body.String(), "An API key cannot call this route. Try: sign in with a browser session.") {
				t.Fatalf("body = %s", w.Body)
			}
		})
	}
}

func TestMiddlewareSessionOnlyMetric(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); _ = provider.Shutdown(t.Context()) })
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	s.sessionOnly = []string{"/api/billing/"}
	r := httptest.NewRequestWithContext(requestContext(t.Context(), testRequest()), "GET", "/api/billing/portal", nil)
	s.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), r)
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	var forbidden int64
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok || m.Name != "apikeys.requests" {
				continue
			}
			for _, point := range sum.DataPoints {
				if v, _ := point.Attributes.Value(attribute.Key("outcome")); v.AsString() == "forbidden" {
					forbidden += point.Value
				}
			}
		}
	}
	if forbidden != 1 {
		t.Fatalf("forbidden = %d", forbidden)
	}
}
