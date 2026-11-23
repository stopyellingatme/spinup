package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSecretsDirectory(t *testing.T) {
	for _, tc := range []struct {
		name, content, want string
		env                 map[string]string
		missing             bool
		wantErr             string
	}{
		{name: "by name", content: "directory\n", want: "directory"},
		{name: "CRLF", content: "directory\r\n", want: "directory"},
		{name: "one newline only", content: "directory\n\n", want: "directory\n"},
		{name: "spaces", content: "directory ", want: "directory "},
		{name: "empty", content: "", want: ""},
		{name: "environment wins", content: "directory", env: map[string]string{"SPINUP_TEST_SECRET": "environment"}, want: "environment"},
		{name: "empty environment wins", content: "directory", env: map[string]string{"SPINUP_TEST_SECRET": ""}, want: ""},
		{name: "both explicit forms still conflict", content: "directory", env: map[string]string{"SPINUP_TEST_SECRET": "environment", "SPINUP_TEST_SECRET_FILE": "explicit"}, wantErr: "both SPINUP_TEST_SECRET and SPINUP_TEST_SECRET_FILE are set"},
		{name: "explicit file wins", content: "directory", env: map[string]string{"SPINUP_TEST_SECRET_FILE": "explicit"}, want: "explicit"},
		{name: "explicit file error wins", content: "directory", env: map[string]string{"SPINUP_TEST_SECRET_FILE": "missing"}, wantErr: "cannot read SPINUP_TEST_SECRET_FILE"},
		{name: "missing file", missing: true, want: "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("SPINUP_ENV", "production")
			t.Setenv("SECRETS_DIR", dir)
			for _, key := range []string{"SPINUP_TEST_SECRET", "SPINUP_TEST_SECRET_FILE"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			for key, value := range tc.env {
				if key == "SPINUP_TEST_SECRET_FILE" {
					value = filepath.Join(dir, value)
					if strings.HasSuffix(value, "explicit") {
						if err := os.WriteFile(value, []byte("explicit\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
				t.Setenv(key, value)
			}
			if !tc.missing {
				if err := os.WriteFile(filepath.Join(dir, "SPINUP_TEST_SECRET"), []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var dst struct {
				Secret string `env:"SPINUP_TEST_SECRET" default:"default"`
			}
			err := Load(&dst)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil || dst.Secret != tc.want {
				t.Fatalf("value = %q, error = %v, want %q", dst.Secret, err, tc.want)
			}
		})
	}
}

func TestSecretsDirectorySafety(t *testing.T) {
	for _, name := range []string{"directory", "symlink", "unreadable", "../outside", "nested/secret", `nested\secret`, ".", ".."} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "SECRET")
			fieldName := "SECRET"
			switch name {
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.WriteFile(path, []byte("private"), 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				fieldName = name
			}
			dst := reflect.New(reflect.StructOf([]reflect.StructField{{Name: "Secret", Type: reflect.TypeFor[string](), Tag: reflect.StructTag("env:" + strconv.Quote(fieldName))}}))
			reads := 0
			err := LoadFrom(dst.Interface(), func(key string) (string, bool) { return dir, key == "SECRETS_DIR" }, func(string) ([]byte, error) {
				reads++
				return nil, fs.ErrPermission
			})
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error = %v", err)
			}
			if name == "unreadable" {
				if reads != 1 || !strings.Contains(err.Error(), path) {
					t.Fatalf("reads = %d, error = %v", reads, err)
				}
			} else if reads != 0 {
				t.Fatalf("read unsafe file %d times", reads)
			}
		})
	}
}

func TestSecretsDirectoryAbsentOrDisabled(t *testing.T) {
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "absent")} {
		t.Run(dir, func(t *testing.T) {
			var dst struct {
				Value    string `env:"VALUE" default:"default"`
				Required string `env:"REQUIRED" required:"true"`
			}
			err := LoadFrom(&dst, func(key string) (string, bool) { return dir, key == "SECRETS_DIR" }, func(string) ([]byte, error) {
				t.Fatal("unexpected read")
				return nil, fs.ErrNotExist
			})
			if err == nil || !strings.Contains(err.Error(), "REQUIRED: required value is missing") || dst.Value != "default" {
				t.Fatalf("value = %q, error = %v", dst.Value, err)
			}
		})
	}
}

func TestSecretsDirectoryAllFieldTypes(t *testing.T) {
	dir := t.TempDir()
	for key, value := range map[string]string{"TEXT": "text", "NUMBER": "42", "ENABLED": "true", "LIST": "a,b", "TIMEOUT": "2s", "RATIO": "1.5", "BIG": "9000000000"} {
		if err := os.WriteFile(filepath.Join(dir, key), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	type Embedded struct {
		Big int64 `env:"BIG"`
	}
	var dst struct {
		Embedded
		Timeout time.Duration `env:"TIMEOUT"`
		Ratio   float64       `env:"RATIO"`
		Text    string        `env:"TEXT"`
		Number  int           `env:"NUMBER"`
		Enabled bool          `env:"ENABLED"`
		List    []string      `env:"LIST"`
	}
	err := LoadFrom(&dst, func(key string) (string, bool) { return dir, key == "SECRETS_DIR" }, os.ReadFile)
	if err != nil || dst.Text != "text" || dst.Number != 42 || dst.Big != 9000000000 || dst.Timeout != 2*time.Second || dst.Ratio != 1.5 || !dst.Enabled || !reflect.DeepEqual(dst.List, []string{"a", "b"}) {
		t.Fatalf("%+v, error = %v", dst, err)
	}
}

func TestSecretsDirectoryDefault(t *testing.T) {
	if defaultSecretsDir != "/run/secrets" {
		t.Fatalf("default = %q", defaultSecretsDir)
	}
	original := defaultSecretsDir
	defaultSecretsDir = t.TempDir()
	t.Cleanup(func() { defaultSecretsDir = original })
	path := filepath.Join(defaultSecretsDir, "SECRET")
	if err := os.WriteFile(path, []byte("default-directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var dst struct {
		Secret string `env:"SECRET"`
	}
	err := LoadFrom(&dst, func(string) (string, bool) { return "", false }, os.ReadFile)
	if err != nil || dst.Secret != "default-directory" {
		t.Fatalf("secret = %q, error = %v", dst.Secret, err)
	}
	dst.Secret = ""
	err = LoadFrom(&dst, func(key string) (string, bool) { return "", key == "SECRETS_DIR" }, func(string) ([]byte, error) {
		t.Fatal("read the default directory when SECRETS_DIR is empty")
		return nil, fs.ErrNotExist
	})
	if err != nil || dst.Secret != "" {
		t.Fatalf("disabled fallback: secret = %q, error = %v", dst.Secret, err)
	}
}

func TestSecretsDirectoryExactName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"SECRET_FILE", "SECRET.old", "UNRELATED"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("wrong"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var dst struct {
		Secret string `env:"SECRET" default:"default"`
	}
	err := LoadFrom(&dst, func(key string) (string, bool) { return dir, key == "SECRETS_DIR" }, func(string) ([]byte, error) {
		t.Fatal("read an unrelated file")
		return nil, fs.ErrNotExist
	})
	if err != nil || dst.Secret != "default" {
		t.Fatalf("secret = %q, error = %v", dst.Secret, err)
	}
}

func TestSecretsDirectoryPrecedenceSkipsInvalidDirectory(t *testing.T) {
	for _, key := range []string{"SECRET", "SECRET_FILE"} {
		var dst struct {
			Secret string `env:"SECRET"`
		}
		err := LoadFrom(&dst, func(k string) (string, bool) {
			if k == "SECRETS_DIR" {
				t.Fatal("looked for the fallback before an explicit value")
			}
			return "explicit", k == key
		}, func(string) ([]byte, error) { return []byte("explicit"), nil })
		if err != nil || dst.Secret != "explicit" {
			t.Fatalf("secret = %q, error = %v", dst.Secret, err)
		}
	}
}

func TestSecretsDirectoryMatchesExplicitFileSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SECRET")
	data := strings.Repeat("s", 1024*1024+1) + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		var dst struct {
			Secret string `env:"SECRET"`
		}
		err := LoadFrom(&dst, func(key string) (string, bool) {
			if key == "SECRETS_DIR" {
				return dir, true
			}
			return path, explicit && key == "SECRET_FILE"
		}, os.ReadFile)
		if err != nil || dst.Secret != strings.TrimSuffix(data, "\n") {
			t.Fatalf("explicit = %v, length = %d, error = %v", explicit, len(dst.Secret), err)
		}
	}
}
