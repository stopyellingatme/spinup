package composelint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
)

func TestLintRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		rule string
		want int
	}{
		{"privileged positive", "services:\n  app:\n    privileged: true\n", "privileged", 1},
		{"privileged negative", "services:\n  app:\n    privileged: false\n", "privileged", 0},
		{"socket short", "services:\n  app:\n    volumes: [/var/run/docker.sock:/sock]\n", "docker-socket", 1},
		{"socket long", "services:\n  app:\n    volumes:\n      - type: bind\n        source: /run/docker.sock\n        target: /sock\n", "docker-socket", 1},
		{"socket negative", "services:\n  app:\n    volumes: [/var/run/data:/data]\n", "docker-socket", 0},
		{"host namespace", "services:\n  app:\n    network_mode: host\n    pid: host\n    ipc: host\n", "host-namespace", 3},
		{"namespace negative", "services:\n  app:\n    network_mode: bridge\n", "host-namespace", 0},
		{"dangerous capability", "services:\n  app:\n    cap_add: [all, cap_sys_admin]\n", "dangerous-cap", 2},
		{"safe capability", "services:\n  app:\n    cap_add: [NET_BIND_SERVICE]\n", "dangerous-cap", 0},
		{"unconfined", "services:\n  app:\n    security_opt: [seccomp:unconfined, apparmor=unconfined]\n", "unconfined", 2},
		{"confined", "services:\n  app:\n    security_opt: [no-new-privileges:true]\n", "unconfined", 0},
		{"public port short", "services:\n  app:\n    ports: [5432:5432, '5433']\n", "public-port", 2},
		{"public port long", "services:\n  app:\n    ports:\n      - target: 5432\n        published: '5432'\n        host_ip: 0.0.0.0\n", "public-port", 1},
		{"private ports", "services:\n  app:\n    ports: ['127.0.0.1:5432:5432', '[::1]:5433:5433']\n", "public-port", 0},
		{"public label", "services:\n  proxy:\n    labels: [spinup.public=true]\n    ports: ['443:443']\n", "public-port", 0},
		{"unpinned image", "services:\n  app:\n    image: alpine:latest\n", "unpinned-image", 1},
		{"pinned image", "services:\n  app:\n    image: alpine@sha256:abc\n", "unpinned-image", 0},
		{"build image", "services:\n  app:\n    image: local:dev\n    build: .\n", "unpinned-image", 0},
		{"literal secret map", "services:\n  app:\n    environment:\n      PASSWORD: value\n      TOKEN_FILE: /run/token\n", "literal-secret", 1},
		{"literal secret list", "services:\n  app:\n    environment: ['API_KEY=value', 'PRIVATE_KEY=${KEY}', 'SECRET=$SECRET']\n", "literal-secret", 1},
		{"secret negative", "services:\n  app:\n    environment:\n      PASSWORD: ${PASSWORD:-default}\n", "literal-secret", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			findings, err := Lint("deploy/compose.yml", []byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			got := 0
			for _, finding := range findings {
				if finding.Rule == test.rule {
					got++
				}
			}
			if got != test.want {
				t.Fatalf("rule %q findings = %d, want %d: %#v", test.rule, got, test.want, findings)
			}
		})
	}
}

func TestLintSuppressionSortingAndYAMLError(t *testing.T) {
	t.Parallel()
	data := "services:\n  zebra: # spinup:allow privileged intended for tests\n    privileged: true\n  alpha:\n    image: alpine:latest\n    # spinup:allow public-port\n    ports: ['80:80']\n"
	findings, err := Lint("deploy/compose.yml", []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 3 {
		t.Fatalf("findings = %#v", findings)
	}
	if findings[0].Service != "alpha" || findings[0].Rule != "bad-suppression" || findings[1].Rule != "public-port" || findings[2].Rule != "unpinned-image" {
		t.Fatalf("findings are not sorted or suppressed: %#v", findings)
	}
	for _, finding := range findings {
		if finding.Rule == "privileged" {
			t.Fatalf("valid suppression did not suppress: %#v", findings)
		}
	}
	if _, err := Lint("deploy/compose.yml", []byte("services: [")); err == nil {
		t.Fatal("Lint accepted invalid YAML")
	}
}

func TestComposeSecurityCheck(t *testing.T) {
	t.Parallel()
	check := composeSecurityCheck(t)
	root := t.TempDir()
	result := check.Run(context.Background(), &doctor.Context{Root: root})
	if result.Status != doctor.Skip {
		t.Fatalf("missing file status = %s", result.Status)
	}

	deploy := filepath.Join(root, "deploy")
	if err := os.Mkdir(deploy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deploy, "compose.yaml"), []byte("services:\n  app:\n    image: alpine@sha256:abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result = check.Run(context.Background(), &doctor.Context{Root: root})
	if result.Status != doctor.OK {
		t.Fatalf("safe file status = %s: %s", result.Status, result.Message)
	}

	var services strings.Builder
	services.WriteString("services:\n")
	for i := range 11 {
		services.WriteString("  app")
		services.WriteString(string(rune('a' + i)))
		services.WriteString(":\n    privileged: true\n")
	}
	if err := os.WriteFile(filepath.Join(deploy, "compose.yaml"), []byte(services.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	result = check.Run(context.Background(), &doctor.Context{Root: root})
	if result.Status != doctor.Fail || strings.Count(result.Message, "\n") != 10 || !strings.Contains(result.Message, "1 more") || result.Try == "" {
		t.Fatalf("unsafe file result = %#v", result)
	}
}

func composeSecurityCheck(t *testing.T) doctor.Check {
	t.Helper()
	for _, check := range doctor.Checks(doctor.Local, true) {
		if check.ID == "compose-security" {
			if check.Title != "Compose security lint" || !check.Quick {
				t.Fatalf("registered check = %#v", check)
			}
			return check
		}
	}
	t.Fatal("compose-security check was not registered")
	return doctor.Check{}
}
