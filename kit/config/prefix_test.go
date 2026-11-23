package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretsDirectoryPrefix(t *testing.T) {
	for _, tc := range []struct {
		name     string
		full     bool
		disabled bool
		want     string
	}{
		{name: "full environment name", full: true, want: "provider-secret"},
		{name: "generic credential ignored"},
		{name: "empty directory disables fallback", full: true, disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			original := defaultSecretsDir
			defaultSecretsDir = dir
			t.Cleanup(func() { defaultSecretsDir = original })
			if err := os.WriteFile(filepath.Join(dir, "CLIENT_SECRET"), []byte("unrelated"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.full {
				if err := os.WriteFile(filepath.Join(dir, "AUTH_OIDC_GITHUB_CLIENT_SECRET"), []byte("provider-secret"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var dst struct {
				Secret string `env:"CLIENT_SECRET"`
			}
			lookup := func(key string) (string, bool) {
				if key == "SECRETS_DIR" {
					if tc.disabled {
						return "", true
					}
					return dir, true
				}
				return "", false
			}
			err := LoadFromPrefix(&dst, "AUTH_OIDC_GITHUB_", lookup, os.ReadFile)
			if err != nil {
				t.Fatal(err)
			}
			if dst.Secret != tc.want {
				t.Fatalf("secret = %q, want %q", dst.Secret, tc.want)
			}
		})
	}
}

func TestLoadFromPrefixPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      map[string]string
		want     string
		conflict bool
	}{
		{name: "directory", want: "directory"},
		{name: "environment", env: map[string]string{"AUTH_OIDC_GITHUB_CLIENT_SECRET": "environment"}, want: "environment"},
		{name: "empty environment", env: map[string]string{"AUTH_OIDC_GITHUB_CLIENT_SECRET": ""}},
		{name: "explicit file", env: map[string]string{"AUTH_OIDC_GITHUB_CLIENT_SECRET_FILE": "explicit"}, want: "file"},
		{name: "conflict", env: map[string]string{"AUTH_OIDC_GITHUB_CLIENT_SECRET": "environment", "AUTH_OIDC_GITHUB_CLIENT_SECRET_FILE": "explicit"}, conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, value := range map[string]string{"AUTH_OIDC_GITHUB_CLIENT_SECRET": "directory", "explicit": "file"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			type Embedded struct {
				Secret string `env:"CLIENT_SECRET"`
			}
			var dst struct{ *Embedded }
			err := LoadFromPrefix(&dst, "AUTH_OIDC_GITHUB_", func(key string) (string, bool) {
				if key == "SECRETS_DIR" {
					return dir, true
				}
				value, ok := tc.env[key]
				if key == "AUTH_OIDC_GITHUB_CLIENT_SECRET_FILE" && ok {
					value = filepath.Join(dir, value)
				}
				return value, ok
			}, os.ReadFile)
			if tc.conflict {
				if err == nil || !strings.Contains(err.Error(), "both AUTH_OIDC_GITHUB_CLIENT_SECRET and AUTH_OIDC_GITHUB_CLIENT_SECRET_FILE are set") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || dst.Secret != tc.want {
				t.Fatalf("secret = %q, error = %v, want %q", dst.Secret, err, tc.want)
			}
		})
	}
}
