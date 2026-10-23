package digitalocean

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// DefaultClientID is the OAuth client ID of spinup. It is empty until the
// maintainers register the client with scripts/do-register-client.sh. A public
// client ID is not a secret. SPINUP_DO_CLIENT_ID overrides it.
const DefaultClientID = ""

// OAuth endpoints (ADR 0017 section 2).
const (
	OAuthIssuer   = "https://cloud.digitalocean.com"
	AuthorizePath = "/v1/oauth/authorize"
	TokenPath     = "/v1/oauth/token"
	RevokePath    = "/v1/oauth/revoke"
	RegisterPath  = "/v1/oauth/register"
)

const defaultAPIBase = "https://api.digitalocean.com"

// Scopes are the OAuth scopes that spinup asks for (ADR 0017 section 4).
var Scopes = []string{
	"account:read", "regions:read", "sizes:read", "image:read", "actions:read",
	"droplet:create", "droplet:read", "droplet:delete",
	"ssh_key:create", "ssh_key:read",
	"firewall:create", "firewall:read", "firewall:update", "firewall:delete",
	"tag:create", "tag:read",
	"domain:read", "domain:create", "domain:update", "domain:delete",
}

// ClientID returns SPINUP_DO_CLIENT_ID when it is set, else DefaultClientID.
func ClientID() string {
	if v := strings.TrimSpace(os.Getenv("SPINUP_DO_CLIENT_ID")); v != "" {
		return v
	}
	return DefaultClientID
}

// APIBase returns the API base URL. SPINUP_DO_API_BASE overrides it.
func APIBase() (string, error) {
	return base("SPINUP_DO_API_BASE", defaultAPIBase)
}

// OAuthBase returns the OAuth base URL. SPINUP_DO_OAUTH_BASE overrides it.
func OAuthBase() (string, error) {
	return base("SPINUP_DO_OAUTH_BASE", OAuthIssuer)
}

func base(env, def string) (string, error) {
	v := strings.TrimSpace(os.Getenv(env))
	if v == "" {
		return def, nil
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("cannot use %s=%q: it is not a URL\nTry: set %s to a full URL such as https://example.com", env, v, env)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback(u.Hostname())) {
		return "", fmt.Errorf("cannot use %s=%q: the base must use https unless the host is a loopback address\nTry: use an https URL, or a loopback host such as http://127.0.0.1:8080", env, v)
	}
	return strings.TrimRight(v, "/"), nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
