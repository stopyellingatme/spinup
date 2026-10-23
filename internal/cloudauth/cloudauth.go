// Package cloudauth signs the user in to a cloud provider with OAuth 2.1 (authorization code
// with PKCE, public client, loopback redirect) and stores the tokens (ADR 0017).
//
// The package is generic. The caller passes the URLs, the client ID, the scopes, the
// environment variable names, and the store file name. The package uses the standard library only.
package cloudauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrReauth means the provider rejected the grant. The user must sign in again.
var ErrReauth = errors.New("the sign-in is no longer valid")

// Config describes one OAuth provider and one public client.
type Config struct {
	AuthorizeURL string
	TokenURL     string
	RevokeURL    string
	ClientID     string
	Scopes       []string
	// HTTP is the client for token calls. The default has a 30 second timeout.
	HTTP *http.Client
	// Now returns the current time. The default is time.Now.
	Now func() time.Time
}

func (c Config) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Token is a stored sign-in.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	// Expiry is the end of the access token. A zero Expiry means the expiry is unknown.
	Expiry   time.Time       `json:"expiry"`
	ClientID string          `json:"client_id,omitempty"`
	Info     json.RawMessage `json:"info,omitempty"`
}

// Valid reports whether the access token is usable at now with the given margin.
// A token with an unknown (zero) expiry is valid while it is not empty.
func (t Token) Valid(now time.Time, margin time.Duration) bool {
	if t.AccessToken == "" {
		return false
	}
	return t.Expiry.IsZero() || now.Add(margin).Before(t.Expiry)
}

func randomB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewVerifier returns a PKCE code verifier made from 32 random bytes.
func NewVerifier() (string, error) { return randomB64(32) }

// NewState returns an OAuth state value made from 32 random bytes.
func NewState() (string, error) { return randomB64(32) }

// Challenge returns the S256 code challenge of a verifier (RFC 7636).
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// problem builds a user-facing error: what failed, why, and a Try line.
func problem(what, why, try string) error {
	if try == "" {
		try = "run the command again"
	}
	return fmt.Errorf("%s: %s\nTry: %s", what, why, try)
}
