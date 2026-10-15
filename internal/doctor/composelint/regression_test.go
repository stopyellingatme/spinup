package composelint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestReviewRegressions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, data, rule string
		want                   int
	}{
		{"title bool", "", "privileged: True", "privileged", 1},
		{"upper bool", "", "privileged: TRUE", "privileged", 1},
		{"string bool", "", "privileged: 'true'", "privileged", 0},
		{"yaml 1.1 bool", "", "privileged: yes", "privileged", 0},
		{"privileged interpolation", "", "privileged: ${PRIV:-true}", "privileged", 1},
		{"namespace interpolation", "", "network_mode: ${NET:-host}\n    pid: $PID\n    ipc: ${IPC}", "host-namespace", 3},
		{"public address", "", "ports: ['203.0.113.5:80:80']", "public-port", 1},
		{"private network address", "", "ports: ['192.168.1.1:80:80']", "public-port", 1},
		{"interpolated host", "", "ports: ['${BIND:-0.0.0.0}:80:80']", "public-port", 1},
		{"interpolated loopback", "", "ports: ['${BIND:-127.0.0.1}:80:80']", "public-port", 1},
		{"public ipv6", "", "ports: ['[2001:db8::1]:80-90:80-90/udp']", "public-port", 1},
		{"loopback forms", "", "ports: ['127.8.9.10:80-90:80-90/udp', '[::1]:80:80', 'localhost:80:80', '127.0.0.1:${PORT:-80}:80']", "public-port", 0},
		{"public long", "", "ports: [{host_ip: '203.0.113.5', target: 80}]", "public-port", 1},
		{"private long", "", "ports: [{host_ip: '::1', target: 80}, {host_ip: '[::1]', target: 81}]", "public-port", 0},
		{"public label", "", "labels: {spinup.public: 'true'}\n    ports: ['203.0.113.5:80:80']", "public-port", 0},
		{"default colon", "", "environment: {DB_PASSWORD: '${DB_PASSWORD:-hunter2}'}", "literal-secret", 1},
		{"default dash", "", "environment: ['DB_PASSWORD=${DB_PASSWORD-hunter2}']", "literal-secret", 1},
		{"empty defaults", "", "environment: {PASSWORD: '${PASSWORD:-}', TOKEN: '${TOKEN-}'}", "literal-secret", 0},
		{"required secret", "", "environment: {PASSWORD: '${PASSWORD:?required}'}", "literal-secret", 0},
		{"dev default yml", "deploy/compose.dev.yml", "environment: {PASSWORD: '${PASSWORD:-hunter2}'}", "literal-secret", 0},
		{"dev default yaml", "deploy/compose.dev.yaml", "environment: {PASSWORD: '${PASSWORD-hunter2}'}", "literal-secret", 0},
		{"dev literal", "compose.dev.yml", "environment: {PASSWORD: hunter2}", "literal-secret", 1},
		{"url credentials", "", "environment: {DATABASE_URL: 'postgres://app:hunter2@db/app'}", "literal-secret", 1},
		{"url list", "", "environment: ['DATABASE_URL=postgres://app:hunter2@db/app']", "literal-secret", 1},
		{"url interpolation", "", "environment: {DATABASE_URL: 'postgres://app:${PASSWORD}@db/app'}", "literal-secret", 0},
		{"url mixed password", "", "environment: {DATABASE_URL: 'postgres://app:literal${PASSWORD}@db/app'}", "literal-secret", 1},
		{"url default", "", "environment: {DATABASE_URL: 'postgres://app:${PASSWORD:-hunter2}@db/app'}", "literal-secret", 1},
		{"url dev default", "compose.dev.yml", "environment: {DATABASE_URL: 'postgres://app:${PASSWORD:-app}@db/app'}", "literal-secret", 0},
		{"socket parent", "", "volumes: ['/var/run:/host/run']", "docker-socket", 1},
		{"socket root", "", "volumes: ['/:/host']", "docker-socket", 1},
		{"socket clean", "", "volumes: ['/var/lib/../run/:/host/run']", "docker-socket", 1},
		{"socket long parent", "", "volumes: [{type: bind, source: /run/, target: /host/run}]", "docker-socket", 1},
		{"socket sibling", "", "volumes: ['/var/runtime:/host/run', '/run/docker.sock.backup:/backup']", "docker-socket", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			file := tt.file
			if file == "" {
				file = "deploy/compose.yml"
			}
			findings, err := Lint(file, []byte("services:\n  app:\n    "+tt.data+"\n"))
			if err != nil {
				t.Fatal(err)
			}
			got := 0
			for _, f := range findings {
				if f.Rule == tt.rule {
					got++
					if strings.Contains(tt.name, "interpolation") && tt.rule != "literal-secret" && !strings.Contains(f.Message, "interpolat") {
						t.Errorf("message does not explain interpolation: %s", f.Message)
					}
				}
			}
			if got != tt.want {
				t.Fatalf("findings = %#v, want %d for %s", findings, tt.want, tt.rule)
			}
		})
	}
}

func TestMergeAndAlias(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, data       string
		privileged, host int
	}{
		{"merge", "x-d: &d {privileged: true, network_mode: host}\nservices:\n  app:\n    <<: *d\n", 1, 1},
		{"service alias", "x-d: &d {privileged: true, network_mode: host}\nservices:\n  app: *d\n", 1, 1},
		{"sequence precedence", "x-a: &a {privileged: false}\nx-b: &b {privileged: true, network_mode: host}\nservices:\n  app:\n    <<: [*a, *b]\n", 0, 1},
		{"own precedence", "x-d: &d {privileged: true, network_mode: host}\nservices:\n  app:\n    privileged: false\n    <<: *d\n    network_mode: bridge\n", 0, 0},
		{"own suppression", "x-d: &d {privileged: true, network_mode: host}\nservices:\n  app:\n    # spinup:allow privileged needed for tests\n    <<: *d # spinup:allow host-namespace needed for tests\n", 0, 0},
		{"nested alias", "x-p: &p TRUE\nx-d: &d {privileged: *p}\nservices:\n  app:\n    <<: *d\n", 1, 0},
		{"nested merge", "x-a: &a {privileged: true}\nx-b: &b {<<: *a, network_mode: host}\nservices:\n  app:\n    <<: *b\n", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			findings, err := Lint("compose.yml", []byte(tt.data))
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, f := range findings {
				counts[f.Rule]++
			}
			if counts["privileged"] != tt.privileged || counts["host-namespace"] != tt.host {
				t.Fatalf("findings = %#v, want privileged=%d host=%d", findings, tt.privileged, tt.host)
			}
		})
	}
}

func TestGoldenDevCompose(t *testing.T) {
	t.Parallel()
	file := "testdata/golden/api/deploy/compose.dev.yml"
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), file))
	if err != nil {
		t.Fatal(err)
	}
	findings, err := Lint(file, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("dev Compose findings = %#v", findings)
	}
}
