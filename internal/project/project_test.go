package project

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ConfigFile), []byte("[app]\nname = 'demo'\nmodule = 'example.com/demo'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := FindRoot(child)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("FindRoot() = %q, want %q", got, root)
	}
}

func TestFindRootNotFound(t *testing.T) {
	t.Parallel()

	_, err := FindRoot(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "Try: run this inside a spinup project, or pass --dir") {
		t.Fatalf("FindRoot() error = %v", err)
	}
}

func TestParseValidateAndExtra(t *testing.T) {
	t.Parallel()

	config, err := Parse([]byte(`behavior_version = 2
modules = ["api"]
default_env = "prod"
future_setting = "kept"

[app]
name = "demo-app"
module = "example.com/demo"

[monitoring]
heartbeat_url = "https://example.test/health"

[env.prod]
host = "example.test"
user = "deploy"
port = 22
protected = true
public_url = "https://example.test"
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if config.Monitoring.Profile != "lite" {
		t.Fatalf("profile = %q, want lite", config.Monitoring.Profile)
	}
	if got := config.Extra["future_setting"]; got != "kept" {
		t.Fatalf("extra future_setting = %#v", got)
	}
}

func TestValidateReportsProblems(t *testing.T) {
	t.Parallel()

	config := &Config{}
	config.App.Name = "Invalid"
	config.Monitoring.Profile = "invalid"
	config.Envs = map[string]Env{"Bad_Name": {}}
	config.DefaultEnv = "missing"

	err := config.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil")
	}
	for _, text := range []string{"app name", "app module", "monitoring profile", "environment name", "default environment"} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("Validate() error = %q, missing %q", err, text)
		}
	}
}

func TestResolveEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  Config
		request string
		want    string
		wantErr bool
	}{
		{
			name:    "explicit",
			config:  Config{Envs: map[string]Env{"dev": {}, "prod": {}}},
			request: "prod",
			want:    "prod",
		},
		{
			name:   "default",
			config: Config{DefaultEnv: "dev", Envs: map[string]Env{"dev": {}}},
			want:   "dev",
		},
		{
			name:   "single",
			config: Config{Envs: map[string]Env{"dev": {}}},
			want:   "dev",
		},
		{
			name:    "ambiguous",
			config:  Config{Envs: map[string]Env{"prod": {}, "dev": {}}},
			wantErr: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			env, err := test.config.ResolveEnv(test.request)
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "Try: pass --env <name>") {
					t.Fatalf("ResolveEnv() error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if env.Name != test.want {
				t.Fatalf("environment = %q, want %q", env.Name, test.want)
			}
		})
	}
}

func TestConfirmProtected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     Env
		confirm Confirm
		wantErr string
	}{
		{name: "not protected", env: Env{Name: "dev"}},
		{name: "ci approval", env: Env{Name: "prod", Protected: true}, confirm: Confirm{Yes: true, CI: true}},
		{name: "not interactive", env: Env{Name: "prod", Protected: true}, confirm: Confirm{}, wantErr: "Try: re-run in a terminal, or pass --yes in CI"},
		{name: "typed match", env: Env{Name: "prod", Host: "example.test", Protected: true}, confirm: Confirm{Interactive: true, In: strings.NewReader("prod\n")}},
		{name: "typed mismatch", env: Env{Name: "prod", Protected: true}, confirm: Confirm{Interactive: true, In: strings.NewReader("dev\n")}, wantErr: "confirmation did not match"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			test.confirm.Out = &output
			err := ConfirmProtected(test.env, "deploy", test.confirm)
			if test.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("ConfirmProtected() error = %v", err)
			}
			if test.confirm.Interactive && !strings.Contains(output.String(), `deploy targets the protected environment "prod"`) {
				t.Fatalf("prompt = %q", output.String())
			}
		})
	}
}

func TestEnvTransport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		transport string
		ok        bool
	}{{"", true}, {"ssh", true}, {"registry", true}, {"ftp", false}} {
		c, err := Parse([]byte("[app]\nname = \"demo\"\nmodule = \"example.com/demo\"\n\n[env.prod]\nhost = \"vm\"\ntransport = \"" + tc.transport + "\"\n"))
		if err != nil {
			t.Fatal(err)
		}
		if c.Envs["prod"].Transport != tc.transport {
			t.Fatalf("transport = %q", c.Envs["prod"].Transport)
		}
		if err := c.Validate(); (err == nil) != tc.ok {
			t.Fatalf("transport %q: Validate() = %v", tc.transport, err)
		}
	}
}
