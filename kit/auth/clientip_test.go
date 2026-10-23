package auth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestParseProxySet(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		items   []string
		trusted []string
		not     []string
	}{
		{[]string{"private"}, []string{"127.0.0.1", "::1", "10.1.2.3", "172.18.0.4", "192.168.1.1", "fd00::1", "100.64.0.1", "169.254.1.1", "fe80::1", "::ffff:10.0.0.1"}, []string{"198.51.100.7", "172.32.0.1", "2001:db8::1", "100.128.0.1"}},
		{[]string{"loopback"}, []string{"127.0.0.1", "::1"}, []string{"10.0.0.1"}},
		{[]string{"none"}, nil, []string{"127.0.0.1", "10.0.0.1"}},
		{nil, nil, []string{"127.0.0.1"}},
		{[]string{"private", "203.0.113.0/24", " 198.51.100.9 "}, []string{"203.0.113.77", "198.51.100.9", "10.0.0.1"}, []string{"198.51.100.10"}},
		{[]string{"172.16.0.0/12"}, []string{"172.20.0.1"}, []string{"192.168.1.1", "127.0.0.1"}},
	} {
		set, err := parseProxySet(tc.items)
		if err != nil {
			t.Fatalf("%v: %v", tc.items, err)
		}
		for _, s := range tc.trusted {
			if !set.trusts(netip.MustParseAddr(s)) {
				t.Errorf("%v does not trust %s", tc.items, s)
			}
		}
		for _, s := range tc.not {
			if set.trusts(netip.MustParseAddr(s)) {
				t.Errorf("%v trusts %s", tc.items, s)
			}
		}
	}
	for _, bad := range []string{"10.0.0.0/33", "proxy", "10.0.0.256"} {
		_, err := parseProxySet([]string{bad})
		if err == nil || !strings.Contains(err.Error(), "\nTry: ") || !strings.Contains(err.Error(), bad) {
			t.Errorf("item %q: error = %v, want the item and a Try line", bad, err)
		}
	}
}

func TestResolveClient(t *testing.T) {
	t.Parallel()
	private, _ := parseProxySet([]string{"private"})
	none, _ := parseProxySet([]string{"none"})
	many := strings.TrimSuffix(strings.Repeat("10.0.0.1, ", 20), ", ")
	for _, tc := range []struct {
		name   string
		set    proxySet
		remote string
		xff    []string
		kind   clientKind
		addr   string
	}{
		{"direct client ignores a forged header", private, "203.0.113.5:4000", []string{"198.51.100.1"}, kindAddr, "203.0.113.5"},
		{"direct client ignores a forged private header", private, "203.0.113.5:4000", []string{"10.0.0.9"}, kindAddr, "203.0.113.5"},
		{"proxy writes the client", private, "172.18.0.4:9000", []string{"198.51.100.1"}, kindAddr, "198.51.100.1"},
		{"forged private entry on the left", private, "172.18.0.4:9000", []string{"10.0.0.9, 203.0.113.5"}, kindAddr, "203.0.113.5"},
		{"forged public entry on the left", private, "172.18.0.4:9000", []string{"198.51.100.1, 203.0.113.5"}, kindAddr, "203.0.113.5"},
		{"another proxy on the right", private, "172.18.0.4:9000", []string{"203.0.113.5, 10.0.0.2"}, kindAddr, "203.0.113.5"},
		{"garbage on the right", private, "172.18.0.4:9000", []string{"203.0.113.5, garbage"}, kindInfra, ""},
		{"all entries trusted", private, "172.18.0.4:9000", []string{"10.0.0.2, 10.0.0.3"}, kindInfra, ""},
		{"no header from a proxy", private, "172.18.0.4:9000", nil, kindInfra, ""},
		{"no header from loopback", private, "127.0.0.1:9000", nil, kindExempt, ""},
		{"loopback peer with loopback entry", private, "[::1]:9000", []string{"127.0.0.1"}, kindExempt, ""},
		{"loopback peer with garbage", private, "127.0.0.1:9000", []string{"garbage"}, kindExempt, ""},
		{"loopback peer with a public entry", private, "127.0.0.1:9000", []string{"198.51.100.1"}, kindAddr, "198.51.100.1"},
		{"proxy that says loopback is not exempt", private, "172.18.0.4:9000", []string{"127.0.0.1"}, kindInfra, ""},
		{"port in the entry", private, "10.0.0.2:1", []string{"198.51.100.1:5555"}, kindAddr, "198.51.100.1"},
		{"ipv6 with brackets and port", private, "10.0.0.2:1", []string{"[2001:db8::1]:443"}, kindAddr, "2001:db8::1"},
		{"ipv6 with brackets", private, "10.0.0.2:1", []string{"[2001:db8::1]"}, kindAddr, "2001:db8::1"},
		{"mapped address", private, "10.0.0.2:1", []string{"::ffff:203.0.113.5"}, kindAddr, "203.0.113.5"},
		{"zone is dropped", private, "10.0.0.2:1", []string{"2001:db8::1%eth0"}, kindAddr, "2001:db8::1"},
		{"several header lines", private, "10.0.0.2:1", []string{"198.51.100.1", "203.0.113.5"}, kindAddr, "203.0.113.5"},
		{"more than 16 entries", private, "10.0.0.2:1", []string{"203.0.113.5, " + many}, kindInfra, ""},
		{"none limits loopback", none, "127.0.0.1:9000", []string{"198.51.100.1"}, kindAddr, "127.0.0.1"},
		{"unix socket peer is loopback", private, "@", nil, kindExempt, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://api.test/", nil)
			r.RemoteAddr = tc.remote
			for _, line := range tc.xff {
				r.Header.Add("X-Forwarded-For", line)
			}
			got := tc.set.resolve(r)
			if got.kind != tc.kind || (tc.addr != "" && got.addr != netip.MustParseAddr(tc.addr)) {
				t.Errorf("resolve = %+v, want kind %d and address %q", got, tc.kind, tc.addr)
			}
		})
	}
}

func TestForwardedEntriesReadsFromTheRight(t *testing.T) {
	t.Parallel()
	got := forwardedEntries([]string{"a, b", "c", strings.Repeat("x,", 100) + "y"})
	if len(got) != maxForwarded || got[0] != "y" || got[1] != "x" {
		t.Errorf("entries = %v", got)
	}
	if got := forwardedEntries([]string{"a, b", "c"}); len(got) != 3 || got[0] != "c" || got[2] != "a" {
		t.Errorf("entries = %v", got)
	}
}

func TestRouteClass(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		method, path string
		class        limitClass
		ok           bool
	}{
		{"POST", "/api/auth/login", classLogin, true},
		{"POST", "/api/auth/register", classRegister, true},
		{"POST", "/api/auth/reset/request", classResetRequest, true},
		{"POST", "/api/auth/reset/confirm", classResetConfirm, true},
		{"GET", "/api/auth/oidc/google/callback", classOIDCCallback, true},
		{"HEAD", "/api/auth/oidc/google/callback", classOIDCCallback, true},
		{"GET", "/api/auth/oidc/g%2Fx/callback", classOIDCCallback, true},
		{"GET", "/api/auth/oidc/google/start", 0, false},
		{"GET", "/api/auth/oidc/providers", 0, false},
		{"GET", "/api/auth/oidc//callback", 0, false},
		{"GET", "/api/auth/oidc/a/b/callback", 0, false},
		{"GET", "/api/auth/login", 0, false},
		{"POST", "/api/auth/logout", 0, false},
		{"GET", "/api/session", 0, false},
		{"POST", "/api/auth/login/", 0, false},
	} {
		r := httptest.NewRequestWithContext(t.Context(), tc.method, "http://api.test"+tc.path, nil)
		class, ok := routeClass(r)
		if ok != tc.ok || (ok && class != tc.class) {
			t.Errorf("%s %s: class %d, %v; want %d, %v", tc.method, tc.path, class, ok, tc.class, tc.ok)
		}
	}
}
