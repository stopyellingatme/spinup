package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadFrom(t *testing.T) {
	t.Parallel()
	type Embedded struct {
		Enabled bool `env:"ENABLED" default:"true"`
	}
	type settings struct {
		Embedded
		Name    string        `env:"NAME" required:"true"`
		Count   int           `env:"COUNT" default:"42"`
		Big     int64         `env:"BIG"`
		Ratio   float64       `env:"RATIO"`
		Timeout time.Duration `env:"TIMEOUT" default:"2s"`
		List    []string      `env:"LIST"`
	}
	for _, tc := range []struct {
		name    string
		env     map[string]string
		wantErr []string
	}{
		{"valid", map[string]string{"NAME": "api", "BIG": "9000000000", "RATIO": "1.5", "LIST": "a, b ,c"}, nil},
		{"file", map[string]string{"NAME_FILE": "secret"}, nil},
		{"both", map[string]string{"NAME": "api", "NAME_FILE": "secret"}, []string{"NAME", "both"}},
		{"missing and invalid", map[string]string{"COUNT": "no", "ENABLED": "no", "BIG": "no", "RATIO": "no", "TIMEOUT": "no"}, []string{"NAME", "COUNT", "ENABLED", "BIG", "RATIO", "TIMEOUT"}},
		{"file failure", map[string]string{"NAME_FILE": "bad"}, []string{"NAME", "read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var dst settings
			err := LoadFrom(&dst, func(k string) (string, bool) { v, ok := tc.env[k]; return v, ok }, func(path string) ([]byte, error) {
				if path == "bad" {
					return nil, errors.New("missing")
				}
				return []byte("api\n"), nil
			})
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatal("expected error")
				}
				for _, s := range append(tc.wantErr, "Try:") {
					if !strings.Contains(err.Error(), s) {
						t.Fatalf("missing %q: %v", s, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if dst.Name != "api" || dst.Count != 42 || !dst.Enabled || dst.Timeout != 2*time.Second {
				t.Fatalf("%+v", dst)
			}
			if tc.name == "valid" && (dst.Big != 9000000000 || dst.Ratio != 1.5 || !reflect.DeepEqual(dst.List, []string{"a", "b", "c"})) {
				t.Fatalf("%+v", dst)
			}
		})
	}
}

func TestBaseAndInvalidDestination(t *testing.T) {
	t.Parallel()
	var b Base
	lookup := func(string) (string, bool) { return "", false }
	if err := LoadFrom(&b, lookup, os.ReadFile); err != nil {
		t.Fatal(err)
	}
	if b.Env != "production" || b.Port != 8080 || b.LogLevel != "info" || b.ShutdownTimeout != 20*time.Second || !b.Behavior(1) || b.Behavior(2) {
		t.Fatalf("%+v", b)
	}
	for _, dst := range []any{nil, Base{}, (*Base)(nil), new(string)} {
		if err := LoadFrom(dst, lookup, os.ReadFile); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("invalid destination: %v", err)
		}
	}
}

func TestLoadEnvironmentDefault(t *testing.T) {
	// Environment and working directory are process-wide.
	for _, tc := range []struct {
		name, env, dotenv, want string
	}{
		{"unset", "", "", "production"},
		{"dotenv development", "", "SPINUP_ENV=dev\n", "dev"},
		{"explicit development", "dev", "", "dev"},
		{"explicit production", "production", "SPINUP_ENV=dev\n", "production"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("SPINUP_ENV", tc.env)
			t.Setenv("SPINUP_ENV_FILE", "")
			if err := os.Unsetenv("SPINUP_ENV_FILE"); err != nil {
				t.Fatal(err)
			}
			if tc.env == "" {
				if err := os.Unsetenv("SPINUP_ENV"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(".env", []byte(tc.dotenv), 0o600); err != nil {
				t.Fatal(err)
			}
			var b Base
			if err := Load(&b); err != nil {
				t.Fatal(err)
			}
			if b.Env != tc.want {
				t.Fatalf("Env = %q, want %q", b.Env, tc.want)
			}
		})
	}
}

func TestLoadDotenv(t *testing.T) {
	// Environment and working directory are process-wide.
	for _, tc := range []struct{ name, env, location, want string }{
		{"current", "dev", ".env", "file"},
		{"parent", "", "../.env", "file"},
		{"production", "production", ".env", "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			api := filepath.Join(root, "api")
			if err := os.Mkdir(api, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(api)
			t.Setenv("SPINUP_ENV", tc.env)
			t.Setenv("SPINUP_TEST_OVERRIDE", "environment")
			if err := os.WriteFile(tc.location, []byte("SPINUP_TEST_VALUE=file\nSPINUP_TEST_OVERRIDE=file\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var dst struct {
				Value    string `env:"SPINUP_TEST_VALUE" default:"default"`
				Override string `env:"SPINUP_TEST_OVERRIDE"`
			}
			if err := Load(&dst); err != nil {
				t.Fatal(err)
			}
			if dst.Value != tc.want || dst.Override != "environment" {
				t.Fatalf("%+v", dst)
			}
		})
	}
}

type embeddedSettings struct {
	Value string `env:"VALUE"`
}

func TestEmbeddedStruct(t *testing.T) {
	t.Parallel()
	var dst struct{ embeddedSettings }
	if err := LoadFrom(&dst, func(key string) (string, bool) { return "loaded", key == "VALUE" }, os.ReadFile); err != nil {
		t.Fatal(err)
	}
	if dst.Value != "loaded" {
		t.Fatalf("embedded value: %q", dst.Value)
	}
}

func TestSecretNewline(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, want string }{{"secret\n", "secret"}, {"secret\r\n", "secret"}, {"secret\n\n", "secret\n"}, {"secret ", "secret "}} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			var dst struct {
				Value string `env:"VALUE"`
			}
			err := LoadFrom(&dst, func(key string) (string, bool) { return "file", key == "VALUE_FILE" }, func(string) ([]byte, error) { return []byte(tc.input), nil })
			if err != nil || dst.Value != tc.want {
				t.Fatalf("%q %v", dst.Value, err)
			}
		})
	}
}

func TestLoadEnvironmentOverridesDotenvSecret(t *testing.T) {
	// Environment and working directory are process-wide.
	for _, tc := range []struct {
		name       string
		shellKey   string
		shellValue string
		dotenv     string
		want       string
	}{
		{"shell file overrides dotenv value", "SPINUP_TEST_SECRET_FILE", "secret", "SPINUP_TEST_SECRET=dotenv", "from-file"},
		{"shell value overrides dotenv file", "SPINUP_TEST_SECRET", "from-shell", "SPINUP_TEST_SECRET_FILE=missing", "from-shell"},
		{"shell value overrides dotenv value", "SPINUP_TEST_SECRET", "from-shell", "SPINUP_TEST_SECRET=dotenv", "from-shell"},
		{"shell file overrides dotenv file", "SPINUP_TEST_SECRET_FILE", "secret", "SPINUP_TEST_SECRET_FILE=missing", "from-file"},
		{"empty shell value overrides dotenv file", "SPINUP_TEST_SECRET", "", "SPINUP_TEST_SECRET_FILE=missing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("SPINUP_ENV", "dev")
			for _, key := range []string{"SPINUP_TEST_SECRET", "SPINUP_TEST_SECRET_FILE"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(tc.shellKey, tc.shellValue)
			if err := os.WriteFile("secret", []byte("from-file\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(".env", []byte(tc.dotenv+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var dst struct {
				Secret string `env:"SPINUP_TEST_SECRET"`
			}
			if err := Load(&dst); err != nil {
				t.Fatal(err)
			}
			if dst.Secret != tc.want {
				t.Fatalf("Secret = %q, want %q", dst.Secret, tc.want)
			}
		})
	}
}
