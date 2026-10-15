package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password length limits, counted in Unicode code points.
const (
	// MinPasswordLength is the shortest password that registration accepts.
	MinPasswordLength = 12
	// MaxPasswordLength is the longest password that registration and login accept.
	MaxPasswordLength = 512
)

// params holds the argon2id cost parameters of one hash.
type params struct {
	memory  uint32 // KiB
	time    uint32 // passes
	threads uint8  // lanes
	saltLen uint32 // bytes
	keyLen  uint32 // bytes
}

// defaultParams is the second recommended option of RFC 9106 (section 4):
// 64 MiB of memory, 3 passes, and 4 lanes, with a 16-byte salt and a 32-byte key.
var defaultParams = params{memory: 64 * 1024, time: 3, threads: 4, saltLen: 16, keyLen: 32}

// Limits for parsed hashes, so a damaged or hostile row cannot exhaust memory.
const (
	maxMemory  = 1 << 21 // KiB (2 GiB)
	maxTime    = 64
	minSaltLen = 8
	maxSaltLen = 64
	minKeyLen  = 16
	maxKeyLen  = 64
)

var (
	b64             = base64.RawStdEncoding
	errInvalidHash  = errors.New("auth: the password hash is not a supported argon2id PHC string")
	errHashCanceled = errors.New("auth: the password hash was canceled")
)

// hashPassword returns the argon2id PHC string of password with a random salt:
// $argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<key> (unpadded standard base64).
func hashPassword(p params, password string) string {
	salt := make([]byte, p.saltLen)
	// crypto/rand.Read never returns an error. It crashes the program instead.
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

// verifyPassword reports whether password matches the PHC string encoded.
// It compares the keys in constant time. It also returns the parameters of
// encoded, so the caller can rehash with newer parameters.
func verifyPassword(encoded, password string) (bool, params, error) {
	p, salt, key, err := parseHash(encoded)
	if err != nil {
		return false, params{}, err
	}
	other := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	return subtle.ConstantTimeCompare(key, other) == 1, p, nil
}

// parseHash parses an argon2id PHC string with version 19 and checks every bound.
func parseHash(encoded string) (params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return params{}, nil, nil, errInvalidHash
	}
	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return params{}, nil, nil, errInvalidHash
	}
	memory, okM := phcUint(fields[0], "m=", maxMemory)
	passes, okT := phcUint(fields[1], "t=", maxTime)
	lanes, okP := phcUint(fields[2], "p=", 255)
	if !okM || !okT || !okP || passes < 1 || lanes < 1 || memory < 8*lanes {
		return params{}, nil, nil, errInvalidHash
	}
	salt, err := b64.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < minSaltLen || len(salt) > maxSaltLen {
		return params{}, nil, nil, errInvalidHash
	}
	key, err := b64.Strict().DecodeString(parts[5])
	if err != nil || len(key) < minKeyLen || len(key) > maxKeyLen {
		return params{}, nil, nil, errInvalidHash
	}
	p := params{memory: uint32(memory), time: uint32(passes), threads: uint8(lanes), saltLen: uint32(len(salt)), keyLen: uint32(len(key))}
	return p, salt, key, nil
}

// phcUint parses "<prefix><decimal>" with a value from 0 to limit.
func phcUint(field, prefix string, limit uint64) (uint64, bool) {
	digits, ok := strings.CutPrefix(field, prefix)
	if !ok || digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return 0, false
	}
	value, err := strconv.ParseUint(digits, 10, 32)
	if err != nil || value > limit {
		return 0, false
	}
	return value, true
}

// passwordLength returns the number of code points in password.
func passwordLength(password string) int { return utf8.RuneCountInString(password) }

// hashSlots limits the number of argon2id computations that run at the same time,
// so a burst of logins cannot exhaust memory (each one uses params.memory KiB).
type hashSlots chan struct{}

// run runs f in a free slot. It returns errHashCanceled when ctx ends first.
func (s hashSlots) run(ctx context.Context, f func()) error {
	select {
	case s <- struct{}{}:
	case <-ctx.Done():
		return errHashCanceled
	}
	defer func() { <-s }()
	f()
	return nil
}
