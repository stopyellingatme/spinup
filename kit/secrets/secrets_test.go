package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	for _, tt := range []struct {
		name, file, direct, want string
		useFile, useDirect, bad  bool
	}{
		{name: "file wins", file: "file\n", direct: "environment", want: "file", useFile: true, useDirect: true},
		{name: "one newline", file: "file\n\n", want: "file\n", useFile: true},
		{name: "windows newline", file: "file\r\n", want: "file", useFile: true},
		{name: "environment", direct: "environment\n", want: "environment", useDirect: true},
		{name: "empty", useDirect: true},
		{name: "missing", bad: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TEST_SECRET", tt.direct)
			t.Setenv("TEST_SECRET_FILE", "")
			if !tt.useDirect {
				if err := os.Unsetenv("TEST_SECRET"); err != nil {
					t.Fatal(err)
				}
			}
			if tt.useFile {
				path := filepath.Join(t.TempDir(), "secret")
				if err := os.WriteFile(path, []byte(tt.file), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("TEST_SECRET_FILE", path)
			} else if err := os.Unsetenv("TEST_SECRET_FILE"); err != nil {
				t.Fatal(err)
			}
			got, err := Load("TEST_SECRET")
			if (err != nil) != tt.bad || got != tt.want {
				t.Fatalf("Load = %q, %v", got, err)
			}
		})
	}
}

func TestLoadUnreadableDoesNotFallBack(t *testing.T) {
	t.Setenv("TEST_SECRET_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("TEST_SECRET", "do-not-expose")
	_, err := Load("TEST_SECRET")
	if err == nil || strings.Contains(err.Error(), "do-not-expose") {
		t.Fatalf("error = %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Must did not panic")
		}
	}()
	Must("TEST_SECRET")
}
