package oracle

import (
	"bytes"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

type hashCase struct {
	Name       string `json:"name"`
	Input      []byte `json:"input"`
	Binary     bool   `json:"binary"`
	Normalized []byte `json:"normalized"`
	Hash       string `json:"hash"`
}

func hashCases(t *testing.T) []hashCase {
	t.Helper()
	inputs := []struct{ name, data string }{
		{"empty", ""}, {"no newline", "hello"}, {"LF", "hello\n"}, {"CRLF", "hello\r\n"},
		{"CR", "hello\r"}, {"trailing newlines", "hello\n\n\r\n"}, {"newlines only", "\r\n\n\r"},
		{"mixed lines", "a\r\nb\rc\n\n"}, {"trailing spaces", "hello \t\n"}, {"unicode", "é界😀\u2028\u2029"},
		{"invalid UTF-8", "\xff\xfe\r\n"}, {"binary", "\x00a\r\n\n"},
		{"NUL at 7999", string(bytes.Repeat([]byte{'a'}, 7999)) + "\x00\r\n"},
		{"NUL at 8000", string(bytes.Repeat([]byte{'a'}, 8000)) + "\x00\r\n"},
	}
	cases := make([]hashCase, 0, len(inputs))
	for _, input := range inputs {
		data := []byte(input.data)
		original := bytes.Clone(data)
		normalized := manifest.Normalize(data)
		hash := manifest.Hash(data)
		if !bytes.Equal(data, original) {
			t.Fatalf("hash mutated %s", input.name)
		}
		if !bytes.Equal(manifest.Normalize(normalized), normalized) {
			t.Fatalf("normalization is not idempotent for %s", input.name)
		}
		cases = append(cases, hashCase{input.name, data, manifest.IsBinary(data), normalized, hash})
	}
	return cases
}

type cleanPathCase struct {
	Input string `json:"input"`
	Clean string `json:"clean"`
	Error string `json:"error"`
}

func cleanPathCases() []cleanPathCase {
	cases := []cleanPathCase{}
	// Preserve the R0 portable corpus and add exact errors and more edge cases.
	for _, item := range pathCases() {
		clean, err := vfs.CleanPath(item.Input)
		cases = append(cases, cleanPathCase{item.Input, clean, errorText(err)})
	}
	for _, input := range []string{"///", "./file", "a/.", "a//b///c/", "a/../../b", "a/.../b", "...", " .hidden/file", ".hidden", "dir/a b.txt", "dir/\u00a0", "NUL.txt/file", "COM¹", "file\u009f", "file\u2028name", "dir/é.txt", "dir/e\u0301.txt", "a/CON /b"} {
		clean, err := vfs.CleanPath(input)
		cases = append(cases, cleanPathCase{input, clean, errorText(err)})
	}
	return cases
}
