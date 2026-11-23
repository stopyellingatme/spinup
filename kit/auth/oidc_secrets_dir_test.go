package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOIDCProviderSecretsDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SECRETS_DIR", dir)
	t.Setenv("SPINUP_ENV", "production")
	t.Setenv("AUTH_OIDC_GITHUB_ISSUER", "https://issuer.example")
	t.Setenv("AUTH_OIDC_GITHUB_CLIENT_ID", "github-client")
	for _, key := range []string{"AUTH_OIDC_GITHUB_CLIENT_SECRET", "AUTH_OIDC_GITHUB_CLIENT_SECRET_FILE", "AUTH_OIDC_GITHUB_SECRETS_DIR"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{"AUTH_OIDC_GITHUB_CLIENT_SECRET": "provider-secret", "CLIENT_SECRET": "unrelated"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := loadOIDCProvider("github")
	if p.err != nil {
		t.Fatal(p.err)
	}
	if p.secret != "provider-secret" {
		t.Fatalf("secret = %q, want provider-secret", p.secret)
	}
	if err := os.Remove(filepath.Join(dir, "AUTH_OIDC_GITHUB_CLIENT_SECRET")); err != nil {
		t.Fatal(err)
	}
	p = loadOIDCProvider("github")
	if p.err != nil || p.secret != "" {
		t.Fatalf("generic secret: secret = %q, error = %v", p.secret, p.err)
	}
}
