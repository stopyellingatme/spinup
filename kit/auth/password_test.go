package auth

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestPasswordDefaultParams(t *testing.T) {
	t.Parallel()
	if defaultParams != (params{memory: 65536, time: 3, threads: 4, saltLen: 16, keyLen: 32}) {
		t.Fatalf("defaultParams = %+v, want RFC 9106 option 2 (docs/auth.md)", defaultParams)
	}
	hash := hashPassword(defaultParams, goodPassword)
	format := regexp.MustCompile(`^\$argon2id\$v=19\$m=65536,t=3,p=4\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)
	if !format.MatchString(hash) {
		t.Fatalf("hash = %q, want the PHC format", hash)
	}
	ok, used, err := verifyPassword(hash, goodPassword)
	if err != nil || !ok || used != defaultParams {
		t.Fatalf("verify = %v, %+v, %v; want a match with the default parameters", ok, used, err)
	}
}

func TestPasswordRoundTrip(t *testing.T) {
	t.Parallel()
	first := hashPassword(testParams, goodPassword)
	second := hashPassword(testParams, goodPassword)
	if first == second {
		t.Error("two hashes of one password are equal: the salt is not random")
	}
	for _, tc := range []struct {
		password string
		want     bool
	}{
		{goodPassword, true},
		{"correct horse battery ", false},
		{"Correct horse battery", false},
		{"", false},
	} {
		ok, used, err := verifyPassword(first, tc.password)
		if err != nil || ok != tc.want || used != testParams {
			t.Errorf("verify(%q) = %v, %+v, %v; want %v", tc.password, ok, used, err, tc.want)
		}
	}
	// A hash from another argon2id implementation (RFC 9106 test-vector style parameters).
	unicode := hashPassword(testParams, "pässwörd 🔑 long enough")
	if ok, _, err := verifyPassword(unicode, "pässwörd 🔑 long enough"); err != nil || !ok {
		t.Errorf("unicode password: %v, %v", ok, err)
	}
}

func TestParseHashRejects(t *testing.T) {
	t.Parallel()
	valid := hashPassword(testParams, goodPassword)
	parts := strings.Split(valid, "$")
	salt, key := parts[4], parts[5]
	for name, encoded := range map[string]string{
		"empty":          "",
		"bcrypt":         "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy",
		"argon2i":        "$argon2i$v=19$m=64,t=1,p=1$" + salt + "$" + key,
		"version 16":     "$argon2id$v=16$m=64,t=1,p=1$" + salt + "$" + key,
		"missing field":  "$argon2id$v=19$m=64,t=1$" + salt + "$" + key,
		"field order":    "$argon2id$v=19$t=1,m=64,p=1$" + salt + "$" + key,
		"zero passes":    "$argon2id$v=19$m=64,t=0,p=1$" + salt + "$" + key,
		"huge memory":    "$argon2id$v=19$m=4194304,t=1,p=1$" + salt + "$" + key,
		"low memory":     "$argon2id$v=19$m=7,t=1,p=1$" + salt + "$" + key,
		"leading zero":   "$argon2id$v=19$m=064,t=1,p=1$" + salt + "$" + key,
		"short salt":     "$argon2id$v=19$m=64,t=1,p=1$AAAA$" + key,
		"short key":      "$argon2id$v=19$m=64,t=1,p=1$" + salt + "$AAAA",
		"padded salt":    "$argon2id$v=19$m=64,t=1,p=1$" + salt + "==$" + key,
		"trailing field": valid + "$x",
	} {
		if _, _, _, err := parseHash(encoded); err == nil {
			t.Errorf("%s: parseHash(%q) succeeded", name, encoded)
		}
		if ok, _, err := verifyPassword(encoded, goodPassword); ok || err == nil {
			t.Errorf("%s: verifyPassword succeeded", name)
		}
	}
}

func TestHashSlotsCancel(t *testing.T) {
	t.Parallel()
	slots := make(hashSlots, 1)
	slots <- struct{}{} // the only slot is busy
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	ran := false
	if err := slots.run(ctx, func() { ran = true }); err == nil || ran {
		t.Fatalf("run = %v, ran = %v; want a cancellation", err, ran)
	}
	<-slots
	if err := slots.run(context.Background(), func() { ran = true }); err != nil || !ran {
		t.Fatalf("run = %v, ran = %v", err, ran)
	}
}

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"alice@example.com", "alice@example.com", true},
		{"  Alice@Example.COM\t", "alice@example.com", true},
		{"a.b+tag@sub.example.org", "a.b+tag@sub.example.org", true},
		{"", "", false},
		{"   ", "", false},
		{"alice", "alice", false},
		{"alice@", "alice@", false},
		{"@example.com", "@example.com", false},
		{"Alice <alice@example.com>", "alice <alice@example.com>", false},
		{"<alice@example.com>", "<alice@example.com>", false},
		{"a@b@example.com", "a@b@example.com", false},
		{`"a b"@example.com`, `"a b"@example.com`, false},
		{"alice@example.com, bob@example.com", "alice@example.com, bob@example.com", false},
		{strings.Repeat("a", 64) + "@" + strings.Repeat("b", 186) + ".com", "", false},
	} {
		got, ok := normalizeEmail(tc.in)
		if ok != tc.ok || (tc.want != "" && got != tc.want) {
			t.Errorf("normalizeEmail(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
