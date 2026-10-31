package project

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/services"
)

func TestValidateServiceSessionAuthModule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modules []string
		auth    string
		wantErr string
	}{
		{
			name:    "session with auth module",
			modules: []string{"api", "auth"},
			auth:    services.AuthSession,
		},
		{
			name:    "session without auth module",
			modules: []string{"api"},
			auth:    services.AuthSession,
			wantErr: `service "ocr": auth = "session" needs the auth module, and this app does not have it. Try: run spinup module add auth (needs the web app), or set auth = "none"`,
		},
		{
			name:    "none without auth module",
			modules: []string{"api"},
			auth:    services.AuthNone,
		},
		{
			name:    "empty without auth module",
			modules: []string{"api"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := validServiceConfig(test.modules, test.auth)
			err := config.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func validServiceConfig(modules []string, auth string) Config {
	config := Config{Modules: modules}
	config.App.Name = "demo"
	config.App.Module = "example.com/demo"
	config.Monitoring.Profile = "lite"
	config.Services = []services.Service{{
		Name:    "ocr",
		Port:    8080,
		DevPort: 8101,
		Health:  "/healthz",
		Public:  "/svc/ocr",
		Auth:    auth,
	}}
	return config
}
