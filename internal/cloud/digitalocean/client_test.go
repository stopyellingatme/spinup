package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean/dofake"
)

const testToken = "test-token"

type sleeper struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *sleeper) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.waits = append(s.waits, d)
	s.mu.Unlock()
	return ctx.Err()
}

// newFake starts a fake and returns a client with a recording Sleep.
func newFake(t *testing.T, o dofake.Options) (*Client, *dofake.Server, *sleeper) {
	t.Helper()
	o.StaticTokens = append(o.StaticTokens, testToken)
	f := dofake.New(o)
	url := f.Start()
	t.Cleanup(f.Close)
	sl := &sleeper{}
	c := New(url, func(context.Context) (string, error) { return testToken, nil })
	c.Sleep = sl.sleep
	return c, f, sl
}

func count(f *dofake.Server, method, prefix string) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

func TestPagination(t *testing.T) {
	var srv *httptest.Server
	var seen []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		page := r.URL.Query().Get("page")
		next := ""
		switch page {
		case "", "1":
			next = srv.URL + "/v2/domains?page=2&per_page=200"
		case "2":
			next = srv.URL + "/v2/domains?page=3&per_page=200"
		}
		name := "d" + page
		if page == "" {
			name = "d1"
		}
		fmt.Fprintf(w, `{"domains":[{"name":%q,"ttl":1800}],"links":{"pages":{"next":%q}},"meta":{"total":3}}`, name, next)
	}))
	defer srv.Close()
	c := New(srv.URL, func(context.Context) (string, error) { return testToken, nil })
	got, err := c.Domains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "d1" || got[2].Name != "d3" {
		t.Fatalf("domains = %+v", got)
	}
	if !strings.Contains(seen[0], "per_page=200") {
		t.Fatalf("first request %q has no per_page=200", seen[0])
	}
}

func TestPaginationIgnoresOtherHost(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"domains":[{"name":"a.com"}],"links":{"pages":{"next":"https://evil.example/v2/domains?page=2"}}}`)
	}))
	defer srv.Close()
	c := New(srv.URL, func(context.Context) (string, error) { return testToken, nil })
	got, err := c.Domains(context.Background())
	if err != nil || len(got) != 1 || calls != 1 {
		t.Fatalf("got %v, err %v, calls %d", got, err, calls)
	}
}

func TestPaginationAgainstFake(t *testing.T) {
	c, f, _ := newFake(t, dofake.Options{})
	f.AddDomain("example.com")
	ctx := context.Background()
	for i := 0; i < 450; i++ {
		_, _, err := c.EnsureRecord(ctx, "example.com", Record{Name: fmt.Sprintf("h%d", i), Data: "192.0.2.1"})
		if err != nil {
			t.Fatal(err)
		}
	}
	recs, err := c.Records(ctx, "example.com", "A", "")
	if err != nil || len(recs) != 450 {
		t.Fatalf("records = %d, err %v", len(recs), err)
	}
}

func TestRateLimitRetry(t *testing.T) {
	c, f, sl := newFake(t, dofake.Options{})
	f.RateLimit(1)
	if _, err := c.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sl.waits) != 1 || sl.waits[0] != time.Second {
		t.Fatalf("waits = %v, want [1s]", sl.waits)
	}
	if n := count(f, "GET", "/v2/account"); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}

func TestRateLimitResetHeader(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("RateLimit-Reset", fmt.Sprint(now.Add(7*time.Second).Unix()))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"account":{"email":"a@b.c"}}`)
	}))
	defer srv.Close()
	sl := &sleeper{}
	c := New(srv.URL, func(context.Context) (string, error) { return testToken, nil })
	c.Sleep, c.Now = sl.sleep, func() time.Time { return now }
	if _, err := c.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sl.waits) != 1 || sl.waits[0] != 7*time.Second {
		t.Fatalf("waits = %v, want [7s]", sl.waits)
	}
}

func TestRetryRules(t *testing.T) {
	tests := []struct {
		name     string
		fail     func(f *dofake.Server)
		call     func(c *Client) error
		wantErr  bool
		requests int
	}{
		{"POST is not retried on 500",
			func(f *dofake.Server) { f.FailNext("POST", "/v2/droplets", 500, 1) },
			func(c *Client) error {
				_, err := c.CreateDroplet(context.Background(), DropletRequest{Name: "x", Region: "nyc3", Size: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64"})
				return err
			}, true, 1},
		{"POST is not retried on 429",
			func(f *dofake.Server) { f.RateLimit(1) },
			func(c *Client) error {
				_, err := c.CreateDroplet(context.Background(), DropletRequest{Name: "x", Region: "nyc3", Size: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64"})
				return err
			}, true, 1},
		{"GET is retried on 502",
			func(f *dofake.Server) { f.FailNext("GET", "/v2/account", 502, 2) },
			func(c *Client) error { _, err := c.Account(context.Background()); return err }, false, 3},
		{"DELETE is retried on 503",
			func(f *dofake.Server) { f.FailNext("DELETE", "/v2/droplets/", 503, 1) },
			func(c *Client) error { return c.DeleteDroplet(context.Background(), 42) }, false, 2},
		{"GET stops after five attempts",
			func(f *dofake.Server) { f.FailNext("GET", "/v2/account", 500, 10) },
			func(c *Client) error { _, err := c.Account(context.Background()); return err }, true, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, f, _ := newFake(t, dofake.Options{})
			tt.fail(f)
			err := tt.call(c)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if n := len(f.Requests()); n != tt.requests {
				t.Fatalf("requests = %d, want %d: %v", n, tt.requests, f.Requests())
			}
		})
	}
}

func TestTokenReadPerRequest(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"account":{}}`)
	}))
	defer srv.Close()
	n := 0
	c := New(srv.URL, func(context.Context) (string, error) { n++; return fmt.Sprintf("tok%d", n), nil })
	for i := 0; i < 2; i++ {
		if _, err := c.Account(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if auth[0] != "Bearer tok1" || auth[1] != "Bearer tok2" {
		t.Fatalf("auth = %v", auth)
	}
}

func TestTokenErrorAndNoLeak(t *testing.T) {
	boom := errors.New("no token")
	c := New("http://127.0.0.1:1", func(context.Context) (string, error) { return "", boom })
	if _, err := c.Account(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// A network error must not contain the token.
	c = New("http://127.0.0.1:1", func(context.Context) (string, error) { return "secret-token-value", nil })
	c.Sleep = (&sleeper{}).sleep
	_, err := c.Account(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret-token-value") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIErrorTryLines(t *testing.T) {
	ctx := context.Background()
	c, f, _ := newFake(t, dofake.Options{DropletLimit: 1})
	req := DropletRequest{Name: "x", Region: "nyc3", Size: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64"}

	bad := New(c.Base, func(context.Context) (string, error) { return "wrong", nil })
	_, err := bad.Account(ctx)
	assertAPIError(t, err, 401, "digital-ocean login")

	f.FailNext("GET", "/v2/account", 403, 1)
	_, err = c.Account(ctx)
	assertAPIError(t, err, 403, "scope")

	if _, err = c.CreateDroplet(ctx, req); err != nil {
		t.Fatal(err)
	}
	_, err = c.CreateDroplet(ctx, req)
	assertAPIError(t, err, 422, "droplet limit")

	f.FailNext("GET", "/v2/account", 500, 5)
	_, err = c.Account(ctx)
	assertAPIError(t, err, 500, "status.digitalocean.com")
	var ae *APIError
	if !errors.As(err, &ae) || ae.RequestID == "" || ae.Method != http.MethodGet || ae.Path != "/v2/account" {
		t.Fatalf("api error = %+v", ae)
	}
}

func assertAPIError(t *testing.T, err error, status int, want string) {
	t.Helper()
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != status {
		t.Fatalf("err = %v, want API error %d", err, status)
	}
	lines := strings.Split(err.Error(), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "Try: ") || !strings.Contains(last, want) {
		t.Fatalf("last line %q does not name %q", last, want)
	}
}

func TestBaseValidation(t *testing.T) {
	tests := []struct {
		env, val string
		want     string
		wantErr  bool
	}{
		{"SPINUP_DO_API_BASE", "", "https://api.digitalocean.com", false},
		{"SPINUP_DO_API_BASE", "https://example.com/", "https://example.com", false},
		{"SPINUP_DO_API_BASE", "http://127.0.0.1:8080", "http://127.0.0.1:8080", false},
		{"SPINUP_DO_API_BASE", "http://localhost:9", "http://localhost:9", false},
		{"SPINUP_DO_API_BASE", "http://[::1]:9", "http://[::1]:9", false},
		{"SPINUP_DO_API_BASE", "http://example.com", "", true},
		{"SPINUP_DO_API_BASE", "http://10.0.0.1", "", true},
		{"SPINUP_DO_API_BASE", "ftp://127.0.0.1", "", true},
		{"SPINUP_DO_API_BASE", "not a url", "", true},
		{"SPINUP_DO_OAUTH_BASE", "", "https://cloud.digitalocean.com", false},
		{"SPINUP_DO_OAUTH_BASE", "http://127.0.0.1:1", "http://127.0.0.1:1", false},
		{"SPINUP_DO_OAUTH_BASE", "http://evil.example", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.env+"="+tt.val, func(t *testing.T) {
			t.Setenv("SPINUP_DO_API_BASE", "")
			t.Setenv("SPINUP_DO_OAUTH_BASE", "")
			t.Setenv(tt.env, tt.val)
			fn := APIBase
			if tt.env == "SPINUP_DO_OAUTH_BASE" {
				fn = OAuthBase
			}
			got, err := fn()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error has no Try line: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClientID(t *testing.T) {
	t.Setenv("SPINUP_DO_CLIENT_ID", "")
	if ClientID() != DefaultClientID {
		t.Fatal("default client ID expected")
	}
	t.Setenv("SPINUP_DO_CLIENT_ID", "abc")
	if ClientID() != "abc" {
		t.Fatal("env client ID expected")
	}
}
