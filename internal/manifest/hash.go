package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

// IsBinary reports whether the first 8000 bytes contain a NUL byte.
func IsBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0
}

// Normalize converts text to LF line endings with exactly one trailing newline.
// Empty and binary content remain unchanged. It never modifies the input.
func Normalize(data []byte) []byte {
	if len(data) == 0 || IsBinary(data) {
		return data
	}
	text := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	text = bytes.ReplaceAll(text, []byte("\r"), []byte("\n"))
	return append(bytes.Clone(bytes.TrimRight(text, "\n")), '\n')
}

// Hash returns the SHA-256 hash of normalized content, prefixed with "sha256:".
func Hash(data []byte) string {
	sum := sha256.Sum256(Normalize(data))
	return "sha256:" + hex.EncodeToString(sum[:])
}
