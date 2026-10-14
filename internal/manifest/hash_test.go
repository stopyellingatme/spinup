package manifest

import (
	"bytes"
	"strconv"
	"testing"
)

func TestNormalize(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, input, want string
		binary            bool
	}{
		{"empty", "", "", false},
		{"missing newline", "hello", "hello\n", false},
		{"CRLF", "hello\r\nworld\r\n", "hello\nworld\n", false},
		{"lone CR", "hello\rworld\r", "hello\nworld\n", false},
		{"extra newlines", "hello\n\n\n", "hello\n", false},
		{"only newlines", "\r\n\r\n", "\n", false},
		{"preserve spaces", "hello \t\n", "hello \t\n", false},
		{"binary", "hello\x00\r\n\n", "hello\x00\r\n\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := []byte(tt.input)
			if got := Normalize(input); string(got) != tt.want {
				t.Errorf("Normalize() = %q, want %q", got, tt.want)
			}
			if string(input) != tt.input {
				t.Fatal("Normalize changed its input")
			}
			if IsBinary(input) != tt.binary {
				t.Errorf("IsBinary() = %v, want %v", IsBinary(input), tt.binary)
			}
		})
	}
}

func TestBinaryWindow(t *testing.T) {
	t.Parallel()
	for _, size := range []int{7999, 8000, 8001} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()
			data := append(bytes.Repeat([]byte{'a'}, size), 0)
			if got := IsBinary(data); got != (size < 8000) {
				t.Errorf("IsBinary() = %v for NUL at %d", got, size)
			}
		})
	}
}

func TestHash(t *testing.T) {
	t.Parallel()
	want := "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	for _, data := range []string{"hello", "hello\n", "hello\r\n", "hello\r", "hello\n\n"} {
		if got := Hash([]byte(data)); got != want {
			t.Errorf("Hash(%q) = %q, want %q", data, got, want)
		}
	}
}

func FuzzNormalize(f *testing.F) {
	for _, data := range [][]byte{nil, []byte("a\r\nb\r\n\n"), []byte("\x00\r\n"), append(bytes.Repeat([]byte("\r\n"), 4000), 0)} {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		before := bytes.Clone(data)
		got := Normalize(data)
		if !bytes.Equal(got, Normalize(got)) {
			t.Fatal("Normalize is not idempotent")
		}
		if !bytes.Equal(data, before) {
			t.Fatal("Normalize changed its input")
		}
		if IsBinary(data) && !bytes.Equal(got, data) {
			t.Fatal("Normalize changed binary content")
		}
	})
}
