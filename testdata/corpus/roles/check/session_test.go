package upgrade_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/db"
)

// TestUpgradedUsersDefaultRole checks existing sessions without a network listener.
func TestUpgradedUsersDefaultRole(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := db.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	a, err := auth.New(pool, auth.WithSecureCookie(false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := a.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	mux := http.NewServeMux()
	a.Routes(mux)
	handler := a.Middleware()(mux)
	for _, email := range []string{"one@example.com", "two@example.com"} {
		var id int64
		var storedRole string
		if err := pool.QueryRow(ctx, "SELECT id, role FROM users WHERE email = $1", email).Scan(&id, &storedRole); err != nil {
			t.Fatal(err)
		}
		if storedRole != "" {
			t.Fatalf("%s: stored role = %q, want empty", email, storedRole)
		}
		expiry := time.Now().Add(time.Hour)
		codec := scs.New().Codec
		data, err := codec.Encode(expiry, map[string]any{"auth.user_id": id})
		if err != nil {
			t.Fatal(err)
		}
		token := "upgrade-existing-user-" + strconv.FormatInt(id, 10)
		hash := sha256.Sum256([]byte(token))
		if _, err := pool.Exec(ctx, "INSERT INTO sessions (token,data,expiry) VALUES ($1,$2,$3) ON CONFLICT (token) DO UPDATE SET data=$2,expiry=$3", base64.RawURLEncoding.EncodeToString(hash[:]), data, expiry); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != http.StatusOK {
			t.Fatalf("session: %d %s", out.Code, out.Body.String())
		}
		var session struct {
			User auth.User `json:"user"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
		if session.User.Email != email || session.User.Role != auth.DefaultRoles().Default {
			t.Fatalf("session user = %+v", session.User)
		}
	}
}
