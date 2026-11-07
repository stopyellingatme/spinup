package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"
	"unicode/utf8"
)

func newToken(prefix string) (token string, hash [32]byte, display string, err error) {
	var raw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", hash, "", storeFailure("create a token", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(raw[:])
	token = prefix + "_" + secret
	return token, sha256.Sum256([]byte(token)), prefix + "_" + secret[:6], nil
}

func validateCreate(in CreateInput) error {
	switch {
	case !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) < 1 || utf8.RuneCountInString(in.Name) > 64 || strings.ContainsAny(in.Name, "\r\n\v\f\u0085\u2028\u2029\x00"):
		return invalidKey("the name must have 1 to 64 characters without a line break")
	case !in.Scope.Valid():
		return invalidKey("the scope must be read or write")
	case in.ExpiresIn < 0 || in.ExpiresIn > 365*24*time.Hour || in.ExpiresIn%(24*time.Hour) != 0:
		return invalidKey("the expiry must be zero or 1 to 365 whole days")
	}
	return nil
}

func invalidKey(why string) error {
	return &storeError{cause: ErrInvalidKey, message: "apikeys: cannot create the key: " + why + ".\nTry: correct the key name, scope, or expiry."}
}
