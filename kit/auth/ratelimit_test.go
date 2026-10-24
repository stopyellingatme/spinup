package auth

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// noEnv is a settings lookup for a machine without environment variables.
func noEnv(string) (string, bool) { return "", false }

// withEnv replaces the environment that New reads, so tests do not depend on the machine.
func withEnv(env map[string]string) Option {
	return func(o *options) {
		o.lookup = func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	}
}

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func v4(s string) client { return client{kind: kindAddr, addr: netip.MustParseAddr(s)} }

func testLimiter(rate Limit, clients int) *limiter {
	var rates [numClasses]Limit
	for c := range rates {
		rates[c] = rate
	}
	return newLimiter(rates, clients, 1)
}

func TestLimiterGCRA(t *testing.T) {
	t.Parallel()
	l := testLimiter(Limit{Burst: 3, Every: 10 * time.Second}, 1024)
	who := v4("198.51.100.7")
	for i := range 3 {
		if _, _, ok := l.take(classLogin, who, t0); !ok {
			t.Fatalf("request %d was refused, want a burst of 3", i+1)
		}
	}
	_, wait, ok := l.take(classLogin, who, t0)
	if ok || wait != 10*time.Second {
		t.Fatalf("4th request: ok %v, wait %s, want refused with 10s", ok, wait)
	}
	// A refused request takes no token: the wait does not grow.
	for range 5 {
		l.take(classLogin, who, t0)
	}
	_, wait, ok = l.take(classLogin, who, t0.Add(4*time.Second))
	if ok || wait != 6*time.Second {
		t.Fatalf("after 4s: ok %v, wait %s, want refused with 6s", ok, wait)
	}
	if _, _, ok := l.take(classLogin, who, t0.Add(10*time.Second)); !ok {
		t.Fatal("one token must return after Every")
	}
	if _, _, ok := l.take(classLogin, who, t0.Add(10*time.Second)); ok {
		t.Fatal("only one token returned")
	}
	// Another class and another client have their own buckets.
	if _, _, ok := l.take(classRegister, who, t0); !ok {
		t.Fatal("register shares the bucket of login")
	}
	if _, _, ok := l.take(classLogin, v4("198.51.100.8"), t0); !ok {
		t.Fatal("a second client shares the bucket")
	}
}

func TestLimiterClockStepBack(t *testing.T) {
	t.Parallel()
	l := testLimiter(Limit{Burst: 2, Every: time.Minute}, 1024)
	who := v4("198.51.100.7")
	late := t0.Add(24 * time.Hour)
	l.take(classLogin, who, late)
	l.take(classLogin, who, late)
	// The clock steps back by a day. The key must wait at most one full bucket.
	_, wait, ok := l.take(classLogin, who, t0)
	if ok || wait > time.Minute {
		t.Fatalf("ok %v, wait %s, want refused for at most 1m", ok, wait)
	}
}

func TestLimiterOff(t *testing.T) {
	t.Parallel()
	l := testLimiter(Limit{Burst: 1, Every: time.Hour, Off: true}, 1024)
	for range 100 {
		if _, _, ok := l.take(classLogin, v4("198.51.100.7"), t0); !ok {
			t.Fatal("an off class refused a request")
		}
	}
	if l.size() != 0 {
		t.Errorf("an off class holds %d entries", l.size())
	}
}

func TestLimiterBoundedMemory(t *testing.T) {
	t.Parallel()
	l := testLimiter(Limit{Burst: 20, Every: 3 * time.Second}, 1024)
	for i := range 100000 {
		l.take(classLogin, client{kind: kindAddr, addr: netip.AddrFrom4([4]byte{10 + byte(i>>16), byte(i >> 8), byte(i), 1})}, t0)
	}
	if n := l.size(); n > 1024 {
		t.Fatalf("the table holds %d entries, want at most 1024", n)
	}
}

func TestLimiterEvictionKeepsPenalisedKeys(t *testing.T) {
	t.Parallel()
	l := testLimiter(Limit{Burst: 2, Every: time.Hour}, 1024)
	hot := v4("198.51.100.99")
	for range 2 {
		l.take(classLogin, hot, t0)
	}
	if _, _, ok := l.take(classLogin, hot, t0); ok {
		t.Fatal("the hot key is not penalised")
	}
	// Cold keys have one token used, so their tat is lower than the tat of the hot key.
	for i := range 20000 {
		l.take(classLogin, client{kind: kindAddr, addr: netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 1})}, t0)
	}
	if _, _, ok := l.take(classLogin, hot, t0); ok {
		t.Fatal("eviction removed the penalised key, so the attacker reset it")
	}
}

func TestLimiterEvictionPrefersFullBuckets(t *testing.T) {
	t.Parallel()
	l := testLimiter(Limit{Burst: 2, Every: time.Second}, 1024)
	hot := v4("198.51.100.99")
	l.take(classLogin, hot, t0.Add(time.Hour))
	l.take(classLogin, hot, t0.Add(time.Hour))
	// 1024 entries from long ago are all full buckets by now.
	for i := range 1023 {
		l.take(classLogin, client{kind: kindAddr, addr: netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 1})}, t0)
	}
	now := t0.Add(time.Hour)
	for i := range 5000 {
		l.take(classLogin, client{kind: kindAddr, addr: netip.AddrFrom4([4]byte{172, byte(i >> 8), byte(i), 1})}, now)
	}
	if _, _, ok := l.take(classLogin, hot, now); ok {
		t.Fatal("the hot key was evicted before the full buckets")
	}
}

func TestLimiterSweepAndRefund(t *testing.T) {
	t.Parallel()
	l := testLimiter(Limit{Burst: 2, Every: time.Minute}, 1024)
	for i := range 10 {
		l.take(classLogin, client{kind: kindAddr, addr: netip.AddrFrom4([4]byte{10, 0, 0, byte(i)})}, t0)
	}
	if l.sweep(t0) != 0 || l.size() != 10 {
		t.Fatalf("a sweep at t0 removed entries: size %d", l.size())
	}
	if removed := l.sweep(t0.Add(2 * time.Minute)); removed != 10 || l.size() != 0 {
		t.Fatalf("removed %d, size %d, want 10 and 0", removed, l.size())
	}
	// A refund gives the token back, once.
	who := v4("198.51.100.7")
	tk1, _, _ := l.take(classLogin, who, t0)
	tk2, _, _ := l.take(classLogin, who, t0)
	if _, _, ok := l.take(classLogin, who, t0); ok {
		t.Fatal("the bucket is not empty")
	}
	l.refund(tk2, t0)
	l.refund(tk2, t0)
	_ = tk1
	if _, _, ok := l.take(classLogin, who, t0); !ok {
		t.Fatal("the refund did not give a token back")
	}
	if _, _, ok := l.take(classLogin, who, t0); ok {
		t.Fatal("a second refund of the same request gave a second token")
	}
}

func TestLimiterConcurrent(t *testing.T) {
	t.Parallel()
	l := testLimiter(Limit{Burst: 5, Every: time.Hour}, 1024)
	var passes atomic.Int64
	var wg sync.WaitGroup
	for g := range 64 {
		wg.Go(func() {
			who := v4(fmt.Sprintf("198.51.100.%d", g%8))
			for range 50 {
				if _, _, ok := l.take(classLogin, who, t0); ok {
					passes.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if got := passes.Load(); got != 8*5 {
		t.Fatalf("%d requests passed, want the budget 40", got)
	}
}

func TestLimiterIPv6Groups(t *testing.T) {
	t.Parallel()
	l := testLimiter(Limit{Burst: 3, Every: time.Hour}, 1024)
	a := client{kind: kindAddr, addr: netip.MustParseAddr("2001:db8:1:2::1")}
	sameNet := client{kind: kindAddr, addr: netip.MustParseAddr("2001:db8:1:2:ffff::9")}
	otherNet := client{kind: kindAddr, addr: netip.MustParseAddr("2001:db8:1:3::1")}
	for range 3 {
		l.take(classLogin, a, t0)
	}
	if _, _, ok := l.take(classLogin, sameNet, t0); ok {
		t.Error("two addresses of one /64 do not share a bucket")
	}
	if _, _, ok := l.take(classLogin, otherNet, t0); !ok {
		t.Error("two /64 networks share a bucket")
	}
}

func TestLimiterIPv6WideBucket(t *testing.T) {
	t.Parallel()
	l := testLimiter(Limit{Burst: 20, Every: 3 * time.Second}, 65536)
	passes := 0
	// 100,000 requests from 100,000 addresses in 65,536 different /64 networks of one /48.
	for i := range 100000 {
		a := netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 1, byte(i >> 8), byte(i), 0, 0, 0, 0, 0, 0, byte(i >> 16), 1})
		if _, _, ok := l.take(classLogin, client{kind: kindAddr, addr: a}, t0); ok {
			passes++
		}
	}
	if want := 20 * wideFactor; passes != want {
		t.Fatalf("%d requests passed, want the /48 budget %d", passes, want)
	}
	// A refused request stores no /64 entry, so the table holds the /48 bucket and the passed /64 buckets.
	if n := l.size(); n > 20*wideFactor+1 {
		t.Errorf("the table holds %d entries, want at most %d", n, 20*wideFactor+1)
	}
}

func TestLimiterInfrastructureBucket(t *testing.T) {
	t.Parallel()
	l := newLimiter([numClasses]Limit{classLogin: {Burst: 2, Every: time.Hour}}, 1024, 1)
	infra := client{kind: kindInfra}
	passes := 0
	for range 100 {
		if _, _, ok := l.take(classLogin, infra, t0); ok {
			passes++
		}
	}
	// The infrastructure bucket is large (burst 50), and it is not unlimited.
	if passes != 50 {
		t.Fatalf("%d requests passed, want 50", passes)
	}
	if _, _, ok := l.take(classLogin, v4("198.51.100.7"), t0); !ok {
		t.Fatal("the infrastructure bucket blocked a client with an address")
	}
}

func BenchmarkTake(b *testing.B) {
	l := testLimiter(Limit{Burst: 1000000, Every: time.Millisecond}, 65536)
	who := v4("198.51.100.7")
	b.ReportAllocs()
	for b.Loop() {
		l.take(classLogin, who, t0)
	}
}

func TestParseLimitSpec(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want Limit
		bad  string
	}{
		{"", Limit{}, ""},
		{"off", Limit{Off: true}, ""},
		{"OFF", Limit{Off: true}, ""},
		{"burst=5", Limit{Burst: 5}, ""},
		{"every=2s", Limit{Every: 2 * time.Second}, ""},
		{"burst=60, every=1s", Limit{Burst: 60, Every: time.Second}, ""},
		{"burst=0", Limit{}, "burst must be 1 to 10000"},
		{"burst=10001", Limit{}, "burst must be 1 to 10000"},
		{"burst=x", Limit{}, "whole number"},
		{"every=0s", Limit{}, "every must be"},
		{"every=48h", Limit{}, "every must be"},
		{"every=3", Limit{}, "duration"},
		{"burst=1,burst=2", Limit{}, "unknown or repeated"},
		{"rate=1", Limit{}, "unknown or repeated"},
		{"20", Limit{}, "use off or burst"},
	} {
		got, err := parseLimitSpec(tc.in)
		switch {
		case tc.bad == "" && (err != nil || got != tc.want):
			t.Errorf("%q: %+v, %v; want %+v", tc.in, got, err, tc.want)
		case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
			t.Errorf("%q: error %v, want %q", tc.in, err, tc.bad)
		}
	}
}

func TestResolveRates(t *testing.T) {
	t.Parallel()
	settings := func(m map[string]string) rateSettings {
		s, err := loadRateSettings(func(k string) (string, bool) { v, ok := m[k]; return v, ok })
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	t.Run("defaults", func(t *testing.T) {
		t.Parallel()
		rates, clients, err := resolveRates(RateLimits{}, settings(nil))
		if err != nil {
			t.Fatal(err)
		}
		if rates[classLogin] != (Limit{20, 3 * time.Second, false}) || rates[classRegister] != (Limit{20, 20 * time.Second, false}) ||
			rates[classResetRequest] != (Limit{10, 30 * time.Second, false}) || rates[classResetConfirm] != (Limit{10, 30 * time.Second, false}) ||
			rates[classOIDCCallback] != (Limit{100, time.Second, false}) || clients != 65536 {
			t.Errorf("rates = %+v, clients %d", rates, clients)
		}
	})
	t.Run("environment beats default, option beats environment", func(t *testing.T) {
		t.Parallel()
		env := settings(map[string]string{"AUTH_RATE_LIMIT_LOGIN": "burst=5,every=1s", "AUTH_RATE_LIMIT_REGISTER": "burst=7", "AUTH_RATE_LIMIT_CLIENTS": "2048"})
		rates, clients, err := resolveRates(RateLimits{Login: Limit{Burst: 9}}, env)
		if err != nil {
			t.Fatal(err)
		}
		if rates[classLogin] != (Limit{9, time.Second, false}) {
			t.Errorf("login = %+v, want burst 9 from the option and every 1s from the environment", rates[classLogin])
		}
		if rates[classRegister] != (Limit{7, 20 * time.Second, false}) || clients != 2048 {
			t.Errorf("register = %+v, clients %d", rates[classRegister], clients)
		}
		_, clients, _ = resolveRates(RateLimits{Clients: 4096}, env)
		if clients != 4096 {
			t.Errorf("clients = %d, want the option 4096", clients)
		}
	})
	t.Run("AUTH_RATE_LIMIT=off", func(t *testing.T) {
		t.Parallel()
		env := settings(map[string]string{"AUTH_RATE_LIMIT": "off", "AUTH_RATE_LIMIT_LOGIN": "burst=5"})
		rates, _, err := resolveRates(RateLimits{Register: Limit{Burst: 3}}, env)
		if err != nil {
			t.Fatal(err)
		}
		for c, l := range rates {
			// The option for register turns that class on again. The others stay off.
			if want := limitClass(c) != classRegister; l.Off != want {
				t.Errorf("%s: Off = %v, want %v", classes[c].name, l.Off, want)
			}
		}
	})
	t.Run("option turns a class off", func(t *testing.T) {
		t.Parallel()
		rates, _, err := resolveRates(RateLimits{Login: Limit{Off: true}}, settings(nil))
		if err != nil || !rates[classLogin].Off || rates[classRegister].Off {
			t.Errorf("rates = %+v, %v", rates, err)
		}
	})
	t.Run("errors name the setting and end with a Try line", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			env  map[string]string
			opt  RateLimits
			want string
		}{
			{map[string]string{"AUTH_RATE_LIMIT_LOGIN": "burst=0"}, RateLimits{}, `AUTH_RATE_LIMIT_LOGIN "burst=0" is not valid`},
			{map[string]string{"AUTH_RATE_LIMIT": "maybe"}, RateLimits{}, `AUTH_RATE_LIMIT "maybe"`},
			{map[string]string{"AUTH_RATE_LIMIT_CLIENTS": "5"}, RateLimits{}, "AUTH_RATE_LIMIT_CLIENTS"},
			{nil, RateLimits{Login: Limit{Burst: -1}}, "negative"},
			{nil, RateLimits{Clients: 3}, "Clients"},
			{map[string]string{"AUTH_RATE_LIMIT_LOGIN": "burst=10000,every=24h"}, RateLimits{}, "at most"},
		} {
			_, _, err := resolveRates(tc.opt, settings(tc.env))
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "\nTry: ") {
				t.Errorf("%v %+v: error = %v, want %q and a Try line", tc.env, tc.opt, err, tc.want)
			}
		}
	})
}

// lazyPool returns a pool that never connects. The tests that use it do not reach the store.
func lazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), "postgres://localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// limitApp is an Auth with stub handlers on every limited route, behind Middleware.
// No request reaches a database.
func limitApp(t *testing.T, opts ...Option) (*Auth, http.Handler, *clock) {
	t.Helper()
	c := &clock{now: t0}
	base := []Option{WithSecureCookie(false), withEnv(nil), func(o *options) { o.params = testParams; o.now = c.Now; o.cleanup = 0 }}
	a, err := New(lazyPool(t), append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux := http.NewServeMux()
	mux.Handle("POST "+pathRegister, ok)
	mux.Handle("POST "+pathLogin, ok)
	mux.Handle("POST "+pathResetRequest, ok)
	mux.Handle("POST "+pathResetConfirm, ok)
	mux.Handle(patternOIDCCallback, ok)
	mux.Handle("GET /api/auth/oidc/{provider}/start", ok)
	mux.Handle("GET /api/session", ok)
	return a, a.Middleware()(mux), c
}

func send(t *testing.T, h http.Handler, method, path, remote string, xff ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, "http://api.test"+path, nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestRateLimitMiddleware(t *testing.T) {
	t.Parallel()
	tight := WithRateLimits(RateLimits{
		Login: Limit{Burst: 2, Every: time.Hour}, Register: Limit{Burst: 3, Every: time.Hour},
		ResetRequest: Limit{Burst: 2, Every: time.Hour}, ResetConfirm: Limit{Burst: 2, Every: time.Hour},
		OIDCCallback: Limit{Burst: 2, Every: time.Hour},
	})
	_, h, clk := limitApp(t, tight)
	const proxy = "172.18.0.4:5000"

	t.Run("429 body and headers", func(t *testing.T) {
		for range 2 {
			if rec := send(t, h, "POST", pathLogin, proxy, "198.51.100.7"); rec.Code != http.StatusNoContent {
				t.Fatalf("status %d, want 204", rec.Code)
			}
		}
		rec := send(t, h, "POST", pathLogin, proxy, "198.51.100.7")
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429", rec.Code)
		}
		if got := rec.Header().Get("Retry-After"); got != "3600" {
			t.Errorf("Retry-After = %q, want 3600", got)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q", got)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Too many sign-in attempts from your network. Try again in 3600 seconds.") || strings.Contains(body, "198.51.100.7") {
			t.Errorf("body = %s", body)
		}
	})
	t.Run("another client is not affected", func(t *testing.T) {
		if rec := send(t, h, "POST", pathLogin, proxy, "198.51.100.8"); rec.Code != http.StatusNoContent {
			t.Fatalf("status %d", rec.Code)
		}
	})
	t.Run("a forged left entry does not change the client", func(t *testing.T) {
		if rec := send(t, h, "POST", pathLogin, proxy, "10.0.0.9, 198.51.100.7"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429", rec.Code)
		}
		if rec := send(t, h, "POST", pathLogin, proxy, "198.51.100.200, 198.51.100.7"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429: the left entry chose a new bucket", rec.Code)
		}
	})
	t.Run("a direct client cannot choose its key", func(t *testing.T) {
		for range 2 {
			send(t, h, "POST", pathLogin, "203.0.113.5:4000", "10.0.0.1")
		}
		if rec := send(t, h, "POST", pathLogin, "203.0.113.5:4000", "198.51.100.77"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429", rec.Code)
		}
	})
	t.Run("loopback without a header is never limited", func(t *testing.T) {
		for range 100 {
			if rec := send(t, h, "POST", pathLogin, "127.0.0.1:5000"); rec.Code != http.StatusNoContent {
				t.Fatalf("status %d", rec.Code)
			}
		}
	})
	t.Run("a proxy without a client address shares one large bucket", func(t *testing.T) {
		passes := 0
		for range 80 {
			if send(t, h, "POST", pathResetConfirm, proxy).Code == http.StatusNoContent {
				passes++
			}
		}
		if passes != 50 {
			t.Fatalf("%d requests passed, want the infrastructure burst 50", passes)
		}
	})
	t.Run("classes are separate", func(t *testing.T) {
		for _, path := range []string{pathRegister, pathResetRequest, pathResetConfirm} {
			if rec := send(t, h, "POST", path, proxy, "198.51.100.7"); rec.Code != http.StatusNoContent {
				t.Fatalf("%s: status %d: another class used the budget", path, rec.Code)
			}
		}
		// reset/request and reset/confirm do not share a bucket.
		send(t, h, "POST", pathResetRequest, proxy, "198.51.100.7")
		if rec := send(t, h, "POST", pathResetRequest, proxy, "198.51.100.7"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("reset request: status %d, want 429", rec.Code)
		}
		if rec := send(t, h, "POST", pathResetConfirm, proxy, "198.51.100.7"); rec.Code != http.StatusNoContent {
			t.Fatalf("reset confirm: status %d, want 204", rec.Code)
		}
	})
	t.Run("routes without a limit", func(t *testing.T) {
		for range 20 {
			for _, path := range []string{"/api/session", "/api/auth/oidc/google/start"} {
				if rec := send(t, h, "GET", path, proxy, "198.51.100.7"); rec.Code != http.StatusNoContent {
					t.Fatalf("%s: status %d", path, rec.Code)
				}
			}
		}
	})
	t.Run("OIDC callback redirects to the login page", func(t *testing.T) {
		path := "/api/auth/oidc/google/callback?code=x&state=y"
		for range 2 {
			if rec := send(t, h, "GET", path, proxy, "198.51.100.9"); rec.Code != http.StatusNoContent {
				t.Fatalf("status %d", rec.Code)
			}
		}
		rec := send(t, h, "GET", path, proxy, "198.51.100.9")
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?error=rate_limited" || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("status %d, Location %q, Retry-After %q", rec.Code, rec.Header().Get("Location"), rec.Header().Get("Retry-After"))
		}
	})
	t.Run("a token returns after Every", func(t *testing.T) {
		clk.Add(time.Hour)
		if rec := send(t, h, "POST", pathLogin, proxy, "198.51.100.7"); rec.Code != http.StatusNoContent {
			t.Fatalf("status %d", rec.Code)
		}
	})
}

func TestRateLimitWithoutLimiter(t *testing.T) {
	t.Parallel()
	_, h, _ := limitApp(t, withEnv(map[string]string{"AUTH_RATE_LIMIT": "off"}))
	for range 100 {
		if rec := send(t, h, "POST", pathLogin, "203.0.113.5:4000"); rec.Code != http.StatusNoContent {
			t.Fatalf("status %d", rec.Code)
		}
	}
	// A code option wins over the environment.
	_, h, _ = limitApp(t, withEnv(map[string]string{"AUTH_RATE_LIMIT": "off"}), WithRateLimits(RateLimits{Login: Limit{Burst: 1, Every: time.Hour}}))
	send(t, h, "POST", pathLogin, "203.0.113.5:4000")
	if rec := send(t, h, "POST", pathLogin, "203.0.113.5:4000"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
	if rec := send(t, h, "POST", pathRegister, "203.0.113.5:4000"); rec.Code != http.StatusNoContent {
		t.Fatalf("register status %d: the class stays off", rec.Code)
	}
}

func TestRateLimitTrustedProxies(t *testing.T) {
	t.Parallel()
	limit := WithRateLimits(RateLimits{Login: Limit{Burst: 1, Every: time.Hour}})
	t.Run("none", func(t *testing.T) {
		t.Parallel()
		_, h, _ := limitApp(t, limit, WithTrustedProxies("none"))
		send(t, h, "POST", pathLogin, "127.0.0.1:1")
		if rec := send(t, h, "POST", pathLogin, "127.0.0.1:1"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status %d: none must limit loopback", rec.Code)
		}
	})
	t.Run("from the environment", func(t *testing.T) {
		t.Parallel()
		_, h, _ := limitApp(t, limit, withEnv(map[string]string{"AUTH_TRUSTED_PROXIES": "172.18.0.0/16"}))
		send(t, h, "POST", pathLogin, "172.18.0.4:1", "198.51.100.1")
		if rec := send(t, h, "POST", pathLogin, "172.18.0.9:1", "198.51.100.1"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429", rec.Code)
		}
		// 10.0.0.2 is not trusted here, so it is a direct client and its header is ignored.
		send(t, h, "POST", pathLogin, "10.0.0.2:1", "198.51.100.1")
		if rec := send(t, h, "POST", pathLogin, "10.0.0.2:1", "198.51.100.2"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429", rec.Code)
		}
	})
	t.Run("option beats environment", func(t *testing.T) {
		t.Parallel()
		_, h, _ := limitApp(t, limit, withEnv(map[string]string{"AUTH_TRUSTED_PROXIES": "none"}), WithTrustedProxies("loopback"))
		for range 5 {
			if rec := send(t, h, "POST", pathLogin, "127.0.0.1:1"); rec.Code != http.StatusNoContent {
				t.Fatalf("status %d", rec.Code)
			}
		}
	})
	t.Run("invalid item", func(t *testing.T) {
		t.Parallel()
		_, err := New(lazyPool(t), withEnv(map[string]string{"AUTH_TRUSTED_PROXIES": "private,10.0.0.0/33"}))
		if err == nil || !strings.Contains(err.Error(), "10.0.0.0/33") || !strings.Contains(err.Error(), "\nTry: ") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestNewRateLimitErrors(t *testing.T) {
	t.Parallel()
	for _, env := range []map[string]string{
		{"AUTH_RATE_LIMIT_LOGIN": "burst=0"},
		{"AUTH_RATE_LIMIT": "perhaps"},
		{"AUTH_RATE_LIMIT_CLIENTS": "many"},
	} {
		a, err := New(lazyPool(t), withEnv(env))
		if err == nil {
			_ = a.Close(context.Background())
			t.Errorf("%v: New succeeded", env)
		} else if !strings.HasPrefix(err.Error(), "auth: cannot start: ") || !strings.Contains(err.Error(), "\nTry: ") {
			t.Errorf("%v: error = %q", env, err)
		}
	}
}

func TestRateLimitSweepAndLog(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return logs.Write(p) }), nil))
	a, h, clk := limitApp(t, WithLogger(logger), WithRateLimits(RateLimits{Login: Limit{Burst: 1, Every: time.Minute}}),
		func(o *options) { o.sweep = 5 * time.Millisecond })
	send(t, h, "POST", pathLogin, "203.0.113.5:4000")
	send(t, h, "POST", pathLogin, "203.0.113.5:4000") // refused
	clk.Add(10 * time.Minute)
	// The sweep resets the counters before it writes the log line, so wait for the line too.
	logged := func() string { mu.Lock(); defer mu.Unlock(); return logs.String() }
	deadline := time.Now().Add(5 * time.Second)
	for a.limits.size() != 0 || a.limits.rejected[classLogin].Load() != 0 || !strings.Contains(logged(), "rate limited requests") {
		if time.Now().After(deadline) {
			t.Fatalf("the sweep did not run: %d entries, %d refusals, log %q", a.limits.size(), a.limits.rejected[classLogin].Load(), logged())
		}
		time.Sleep(5 * time.Millisecond)
	}
	out := logged()
	if !strings.Contains(out, "rate limited requests in the last minute") || !strings.Contains(out, "login=1") || !strings.Contains(out, "try=") {
		t.Errorf("log = %q", out)
	}
	if strings.Contains(out, "203.0.113.5") {
		t.Errorf("the log has a client address: %q", out)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestRateLimitWarnsOnceForInfrastructure(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return logs.Write(p) }), nil))
	_, h, _ := limitApp(t, WithLogger(logger))
	for range 5 {
		send(t, h, "POST", pathLogin, "172.18.0.4:5000")
	}
	mu.Lock()
	defer mu.Unlock()
	if n := strings.Count(logs.String(), "share one rate limit"); n != 1 {
		t.Errorf("%d warnings, want 1: %q", n, logs.String())
	}
	if strings.Contains(logs.String(), "172.18.0.4") {
		t.Errorf("the log has an address: %q", logs.String())
	}
}

func TestRateLimitHandlers(t *testing.T) {
	t.Parallel()
	const peer = "192.0.2.1:1234"
	t.Run("login 429 reaches no query", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, withEnv(nil), WithRateLimits(RateLimits{Login: Limit{Burst: 2, Every: time.Hour}}))
		for i := range 2 {
			rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", remote: peer, body: creds(fmt.Sprintf("user%d@example.com", i), goodPassword)})
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("login %d: status %d", i, rec.Code)
			}
		}
		var rows int
		count := func() int {
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM login_attempts`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			return rows
		}
		before := count()
		acquires := f.pool.Stat().AcquireCount()
		// A bogus session cookie must not cost a store lookup either.
		for range 20 {
			rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", remote: peer, cookie: "bogus", body: creds("user9@example.com", goodPassword)})
			if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "3600" {
				t.Fatalf("status %d, Retry-After %q, want 429 and 3600", rec.Code, rec.Header().Get("Retry-After"))
			}
		}
		if got := f.pool.Stat().AcquireCount() - acquires; got != 0 {
			t.Errorf("the refused requests acquired %d connections, want 0", got)
		}
		if after := count(); after != before {
			t.Errorf("login_attempts rows %d -> %d: a refused request reached the lockout", before, after)
		}
		other := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", remote: "192.0.2.9:1", body: creds("user9@example.com", goodPassword)})
		if other.Code != http.StatusUnauthorized {
			t.Errorf("another client: status %d, want 401", other.Code)
		}
	})
	t.Run("a 503 from the hash queue gives the token back", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, withEnv(nil), WithHashLimits(1, 0, time.Minute), WithRateLimits(RateLimits{Login: Limit{Burst: 1, Every: time.Hour}}))
		f.auth.slots.admitted <- struct{}{} // the only place is taken
		for i := range 3 {
			rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", remote: peer, body: creds("busy@example.com", goodPassword)})
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("request %d: status %d, want 503", i, rec.Code)
			}
		}
		<-f.auth.slots.admitted
		if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", remote: peer, body: creds("busy@example.com", goodPassword)}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", rec.Code)
		}
		if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", remote: peer, body: creds("busy@example.com", goodPassword)}); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429", rec.Code)
		}
	})
	t.Run("register has its own bucket", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, withEnv(nil), WithRateLimits(RateLimits{Register: Limit{Burst: 1, Every: time.Hour}}))
		if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/register", remote: peer, body: creds("one@example.com", goodPassword)}); rec.Code != http.StatusCreated {
			t.Fatalf("status %d", rec.Code)
		}
		rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/register", remote: peer, body: creds("two@example.com", goodPassword)})
		if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "sign-up attempts") {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", remote: peer, body: creds("one@example.com", goodPassword)}); rec.Code != http.StatusOK {
			t.Fatalf("login status %d", rec.Code)
		}
	})
}
