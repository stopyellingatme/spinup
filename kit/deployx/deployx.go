package deployx

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

// Options configures the deployment information handler.
type Options struct {
	// Version identifies the application build.
	Version string
	// TokenFile contains the shared bearer token. Empty uses DEPLOYX_TOKEN_FILE.
	TokenFile string
	// MigrationVersion returns the current database version. Nil reports an empty version.
	MigrationVersion func(context.Context) (string, error)
}

// Info is the deployment contract response.
type Info struct {
	// Version identifies the application build.
	Version string `json:"version"`
	// GitSHA is the embedded VCS revision.
	GitSHA string `json:"git_sha"`
	// MigrationVersion is the current database migration version.
	MigrationVersion string `json:"migration_version"`
	// StartedAt is the UTC time when the handler was constructed.
	StartedAt time.Time `json:"started_at"`
}

// GitSHA returns the VCS revision embedded by the Go compiler, or an empty string.
func GitSHA() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return ""
}

// New returns a handler for GET /_deploy/info. It trusts only the direct peer address.
// Direct private and loopback peers are internal. Forwarded requests require a bearer token.
func New(o Options) http.Handler {
	started := time.Now().UTC()
	sha := GitSHA()
	if o.TokenFile == "" {
		o.TokenFile = os.Getenv("DEPLOYX_TOKEN_FILE")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_deploy/info", func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		addr, parseErr := netip.ParseAddr(host)
		// Proxy addresses do not identify the original client. Require a token whenever
		// a proxy marks a request as forwarded, without trusting a supplied address.
		forwarded := r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != ""
		internal := !forwarded && err == nil && parseErr == nil && (addr.Unmap().IsPrivate() || addr.Unmap().IsLoopback())
		authorized := false
		if !internal && o.TokenFile != "" {
			data, readErr := os.ReadFile(o.TokenFile)
			token := strings.TrimSpace(string(data))
			given, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			authorized = readErr == nil && token != "" && bearer && subtle.ConstantTimeCompare([]byte(token), []byte(given)) == 1
		}
		if !internal && !authorized {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		info := Info{Version: o.Version, GitSHA: sha, StartedAt: started}
		if o.MigrationVersion != nil {
			version, err := o.MigrationVersion(r.Context())
			if err != nil {
				http.Error(w, "migration version unavailable", http.StatusServiceUnavailable)
				return
			}
			info.MigrationVersion = version
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(info)
	})
	return mux
}
