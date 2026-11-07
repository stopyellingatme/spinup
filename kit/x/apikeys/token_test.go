package apikeys

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Prefix: "key", DefaultPlan: "free", MaxKeys: 10, Plans: map[string]Plan{"free": {PerMinute: 60, PerDay: 1000}}}
}

func TestToken(t *testing.T) {
	token, hash, display, err := newToken("key")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "key_"))
	if err != nil || len(raw) != 32 || len(token) != 47 {
		t.Fatalf("invalid token format: %v", err)
	}
	if hash != sha256.Sum256([]byte(token)) {
		t.Fatal("digest is not SHA256 of the whole token")
	}
	if display != token[:10] {
		t.Fatal("incorrect display")
	}
	other, _, _, err := newToken("key")
	if err != nil || token == other {
		t.Fatal("tokens must be independent")
	}
}

func TestCreateValidation(t *testing.T) {
	for _, in := range []CreateInput{
		{Name: "", Scope: ScopeRead}, {Name: "a\nb", Scope: ScopeRead}, {Name: "a\rb", Scope: ScopeRead}, {Name: "a\u2028b", Scope: ScopeRead}, {Name: "a\u0085b", Scope: ScopeRead},
		{Name: strings.Repeat("é", 65), Scope: ScopeRead}, {Name: "a", Scope: "other"},
		{Name: "a", Scope: ScopeRead, ExpiresIn: time.Hour}, {Name: "a", Scope: ScopeRead, ExpiresIn: -24 * time.Hour},
		{Name: "a", Scope: ScopeRead, ExpiresIn: 366 * 24 * time.Hour},
	} {
		if err := validateCreate(in); !errors.Is(err, ErrInvalidKey) || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("input %+v: %v", in, err)
		}
	}
	for _, d := range []time.Duration{0, 24 * time.Hour, 365 * 24 * time.Hour} {
		if err := validateCreate(CreateInput{Name: strings.Repeat("é", 64), Scope: ScopeWrite, ExpiresIn: d}); err != nil {
			t.Fatal(err)
		}
	}
}
