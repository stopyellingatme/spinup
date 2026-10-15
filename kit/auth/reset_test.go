package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/mail"
)

func resetFixture(t *testing.T, m mail.Mailer) *fixture {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("reset integration tests require DATABASE_URL")
	}
	f := newFixture(t)
	// Read the real pack migration. The file set does not permit a testdata copy.
	sql, err := os.ReadFile("../../internal/templates/packs/auth/files/api/db/migrations/00003_password_resets.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(sql), "-- +goose Down")
	if _, err := f.pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	f.auth.Routes(mux)
	if err := f.auth.ResetRoutes(mux, WithMailer(m), WithResetFrom("app@example.com"), WithResetURL("https://app.example/reset")); err != nil {
		t.Fatal(err)
	}
	f.handler = httpx.Chain(mux, f.auth.Middleware())
	return f
}

func TestResetRoutesWithoutMailer(t *testing.T) {
	t.Parallel()
	a := &Auth{}
	mux := http.NewServeMux()
	if err := a.ResetRoutes(mux); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/auth/reset/request", "/api/auth/reset/confirm"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil))
		r := rec.Code
		if r != http.StatusNotFound {
			t.Errorf("%s: status %d", path, r)
		}
	}
}

func TestResetSingleUseAndSessions(t *testing.T) {
	t.Parallel()
	capture := &mail.Capture{}
	f := resetFixture(t, capture)
	first := f.register(t, "reset@example.com")
	second := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("reset@example.com", goodPassword)})
	secondCookie := sessionCookie(second).Value
	other := f.register(t, "other@example.com")
	for range DefaultLockoutThreshold {
		f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("reset@example.com", "incorrect password")})
	}
	rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/request", body: map[string]string{"email": " RESET@example.com "}})
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	token := waitResetToken(t, capture)
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token: %v, bytes %d", err, len(raw))
	}
	digest := sha256.Sum256(raw)
	var stored []byte
	var expiry time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT token_hash,expires_at FROM password_resets`).Scan(&stored, &expiry); err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(digest[:]) || !expiry.Equal(f.clock.Now().Add(time.Hour)) {
		t.Fatal("wrong token digest or expiry")
	}
	for _, pw := range []string{"short", strings.Repeat("x", MaxPasswordLength+1)} {
		if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/confirm", body: map[string]string{"token": token, "password": pw}}); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("password validation: %d", rec.Code)
		}
	}
	rec = f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/confirm", cookie: first, body: map[string]string{"token": token, "password": "new correct password"}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	for _, cookie := range []string{first, secondCookie} {
		if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: cookie}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("session survived: %d", rec.Code)
		}
	}
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: other}); rec.Code != http.StatusOK {
		t.Fatalf("other session removed: %d", rec.Code)
	}
	if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/confirm", body: map[string]string{"token": token, "password": "another correct password"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("reused token: %d", rec.Code)
	}
	if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("reset@example.com", "new correct password")}); rec.Code != http.StatusOK {
		t.Fatalf("new login: %d %s", rec.Code, rec.Body)
	}
}

func TestResetExpiredAndConcurrent(t *testing.T) {
	t.Parallel()
	for _, expired := range []bool{true, false} {
		t.Run(map[bool]string{true: "expired", false: "concurrent"}[expired], func(t *testing.T) {
			t.Parallel()
			c := &mail.Capture{}
			f := resetFixture(t, c)
			f.register(t, "reset@example.com")
			f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/request", body: map[string]string{"email": "reset@example.com"}})
			token := waitResetToken(t, c)
			if expired {
				f.clock.Add(time.Hour)
			}
			var wg sync.WaitGroup
			codes := make(chan int, 2)
			for range 2 {
				wg.Go(func() {
					rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/confirm", body: map[string]string{"token": token, "password": "new correct password"}})
					codes <- rec.Code
				})
			}
			wg.Wait()
			close(codes)
			successes := 0
			for code := range codes {
				if code == http.StatusNoContent {
					successes++
				} else if code != http.StatusUnprocessableEntity {
					t.Errorf("confirm: %d", code)
				}
			}
			want := 1
			if expired {
				want = 0
			}
			if successes != want {
				t.Fatalf("successes = %d, want %d", successes, want)
			}
		})
	}
}

func waitResetToken(t *testing.T, c *mail.Capture) string {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("reset delivery did not finish")
			return ""
		case <-tick.C:
			if messages := c.Messages(); len(messages) > 0 {
				words := strings.Fields(messages[0].Text)
				for _, word := range words {
					if strings.HasPrefix(word, "https://app.example/reset/") {
						return strings.TrimPrefix(word, "https://app.example/reset/")
					}
				}
				t.Fatal("message has no trusted reset URL")
			}
		}
	}
}

func TestResetRequestDoesNotWaitForDelivery(t *testing.T) {
	t.Parallel()
	sender := &blockedResetMailer{started: make(chan struct{}), release: make(chan struct{})}
	f := resetFixture(t, sender)
	defer close(sender.release)
	f.register(t, "reset@example.com")
	rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/request", body: map[string]string{"email": "reset@example.com"}})
	if rec.Code != http.StatusAccepted {
		t.Fatal(rec.Code)
	}
	select {
	case <-sender.started:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not start")
	}
	for _, email := range []string{"reset@example.com", "unknown@example.com", "invalid"} {
		rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/request", body: map[string]string{"email": email}})
		if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
			t.Fatalf("request %q: %d %s", email, rec.Code, rec.Body)
		}
	}
}

type blockedResetMailer struct {
	started, release chan struct{}
	once             sync.Once
}

func (m *blockedResetMailer) Send(ctx context.Context, _ mail.Message) error {
	m.once.Do(func() { close(m.started) })
	select {
	case <-m.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestResetRequestLimits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		requests   int
		sameEmail  bool
		concurrent bool
		want       int
	}{
		{name: "shared web peer", requests: 10, want: 10},
		{name: "per email", requests: 10, sameEmail: true, want: 5},
		{name: "global", requests: 110, want: 100},
		{name: "concurrent global", requests: 120, concurrent: true, want: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := resetFixture(t, &mail.Capture{})
			// Inspect the admission queue without asynchronous account lookup or delivery.
			h := &resetHandler{auth: f.auth, requests: make(chan string, tc.requests+1)}
			send := func(i int) {
				email := fmt.Sprintf("unknown%d@example.com", i)
				if tc.sameEmail {
					email = " UNKNOWN@example.com "
				}
				r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/auth/reset/request", strings.NewReader(`{"email":"`+email+`"}`))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
				r.RemoteAddr = "203.0.113.1:12345"
				rec := httptest.NewRecorder()
				h.request(rec, r)
				if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
					t.Errorf("request %d: %d %s", i, rec.Code, rec.Body)
				}
			}
			var wg sync.WaitGroup
			for i := range tc.requests {
				if tc.concurrent {
					wg.Go(func() { send(i) })
				} else {
					send(i)
				}
			}
			wg.Wait()
			if got := len(h.requests); got != tc.want {
				t.Fatalf("admitted %d requests, want %d", got, tc.want)
			}
			var reserved int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM login_attempts WHERE email = 'reset-global'`).Scan(&reserved); err != nil {
				t.Fatal(err)
			}
			if reserved != tc.want {
				t.Fatalf("global reservations: %d, want %d", reserved, tc.want)
			}
			// Both buckets admit requests again at the window boundary.
			f.clock.Add(DefaultLockoutWindow)
			send(0)
			if got := len(h.requests); got != tc.want+1 {
				t.Fatalf("admitted after window: %d, want %d", got, tc.want+1)
			}
		})
	}
}
