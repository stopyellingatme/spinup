package maintain

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

var maintenanceNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func TestPatch(t *testing.T) {
	t.Parallel()
	t.Run("plan JSON", func(t *testing.T) {
		t.Parallel()
		root := maintenanceProject(t, "", "")
		vm := &remote.Fake{}
		vm.Expect("apt", "list", "--upgradable").Return("base-files/stable 1.0\n", "", 0)
		out, err := runMaintenance(t, root, Options{Action: "patch", Plan: true, JSON: true}, Deps{Remote: vm})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Upgradable []string `json:"upgradable"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Upgradable) != 2 {
			t.Fatalf("patch --plan JSON = %q, %v", out.String(), err)
		}
		vm.AssertDone(t)
	})
	t.Run("installs updates with sudo environment", func(t *testing.T) {
		t.Parallel()
		root := maintenanceProject(t, "", "")
		vm := &remote.Fake{}
		vm.Expect("sudo", "apt-get", "update")
		vm.Expect("sudo", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "upgrade")
		vm.Expect("test", "-e", "/var/run/reboot-required").Return("", "", 1)
		out, err := runMaintenance(t, root, Options{Action: "patch", JSON: true}, Deps{Remote: vm})
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]bool
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["reboot_required"] {
			t.Fatalf("patch JSON = %q, %v", out.String(), err)
		}
		vm.AssertDone(t)
	})
	t.Run("refuses a reboot outside the window", func(t *testing.T) {
		t.Parallel()
		root := maintenanceProject(t, "", "Sun 02:00-05:00")
		vm := &remote.Fake{}
		vm.Expect("sudo", "apt-get", "update")
		vm.Expect("sudo", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "upgrade")
		vm.Expect("test", "-e", "/var/run/reboot-required")
		_, err := runMaintenance(t, root, Options{Action: "patch", Reboot: true}, Deps{Remote: vm, Now: func() time.Time { return maintenanceNow }})
		if err == nil || !strings.Contains(err.Error(), "outside the maintenance window") || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("patch reboot error = %v", err)
		}
		vm.AssertDone(t)
	})
	t.Run("retries until the VM is ready", func(t *testing.T) {
		t.Parallel()
		root := maintenanceProject(t, "", "")
		vm := &remote.Fake{}
		vm.Expect("sudo", "apt-get", "update")
		vm.Expect("sudo", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "upgrade")
		vm.Expect("test", "-e", "/var/run/reboot-required")
		vm.Expect("sudo", "reboot")
		vm.Expect("docker", "compose", "-p", "todo", "ps").Return("", "", 255)
		vm.Expect("docker", "compose", "-p", "todo", "ps").Return("NAME STATUS\n", "", 0)
		out, err := runMaintenance(t, root, Options{Action: "patch", Reboot: true, Now: true, JSON: true}, Deps{Remote: vm, Sleep: func(context.Context, time.Duration) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]bool
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || !got["rebooted"] {
			t.Fatalf("patch reboot JSON = %q, %v", out.String(), err)
		}
		vm.AssertDone(t)
	})
}

func TestVulnScan(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, report, failOn string
		code                 int
		critical, high       int
	}{
		{"high and critical fail", `{"Results":[{"Vulnerabilities":[{"Severity":"CRITICAL"},{"Severity":"HIGH"}]}]}`, "", ui.ExitDrift, 1, 1},
		{"critical ignores high", `{"Results":[{"Vulnerabilities":[{"Severity":"HIGH"}]}]}`, "critical", ui.ExitOK, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := maintenanceProject(t, "", "")
			vm := &remote.Fake{}
			vm.Expect("docker", "ps", "--filter", "label=com.docker.compose.project=todo", "--format", "{{.Image}}").Return("todo-api:release\n", "", 0)
			vm.Expect("docker", "run", "--rm", "-v", "/var/run/docker.sock:/var/run/docker.sock:ro", "-v", "spinup-trivy-cache:/root/.cache/").Return(tc.report, "", 0)
			for _, project := range []string{"todo-blue", "todo-green", "todo-monitoring"} {
				vm.Expect("docker", "ps", "--filter", "label=com.docker.compose.project="+project, "--format", "{{.Image}}")
			}
			out, err := runMaintenance(t, root, Options{Action: "vuln-scan", JSON: true, FailOn: tc.failOn}, Deps{Remote: vm})
			if ui.ExitCode(err) != tc.code {
				t.Fatalf("vuln-scan exit code = %d, err = %v", ui.ExitCode(err), err)
			}
			var got []finding
			if jsonErr := json.Unmarshal(out.Bytes(), &got); jsonErr != nil || len(got) != 1 || got[0].Critical != tc.critical || got[0].High != tc.high {
				t.Fatalf("vuln-scan JSON = %q, %v", out.String(), jsonErr)
			}
			calls := vm.Calls()
			if got := calls[1].Argv[len(calls[1].Argv)-1]; got != "todo-api:release" {
				t.Fatalf("Trivy target = %q", got)
			}
			vm.AssertDone(t)
		})
	}
}

func TestPrunePlan(t *testing.T) {
	t.Parallel()
	root := maintenanceProject(t, "", "")
	vm := &remote.Fake{}
	vm.Expect("sh", "-c").Return("[]", "", 0)
	vm.Expect("sh", "-ec").Return("todo-api:old\n", "", 0)
	out, err := runMaintenance(t, root, Options{Action: "prune", Plan: true, JSON: true}, Deps{Remote: vm})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Images []string `json:"images"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Images) != 1 || got.Images[0] != "todo-api:old" {
		t.Fatalf("prune --plan JSON = %q, %v", out.String(), err)
	}
	calls := vm.Calls()
	if len(calls) != 2 || !strings.Contains(calls[1].Argv[2], `case "$image" in "$app"-*`) {
		t.Fatalf("prune script does not limit images to the app: %#v", calls)
	}
	vm.AssertDone(t)
}

func TestVacuum(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, table, query string }{
		{"analyze", "", "VACUUM (ANALYZE)"},
		{"full", `events"archive`, `VACUUM FULL "events""archive"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := maintenanceProject(t, "", "")
			vm := &remote.Fake{}
			vm.Expect("docker", "ps", "--filter", "label=com.docker.compose.project=todo", "--filter", "label=com.docker.compose.service=postgres", "--format", "{{.ID}}").Return("postgres-id\n", "", 0)
			vm.Expect("docker", "exec", "postgres-id", "printenv", "POSTGRES_USER", "POSTGRES_DB").Return("app-user\napp-db\n", "", 0)
			vm.Expect("docker", "exec", "postgres-id", "psql", "-U", "app-user", "-d", "app-db", "-c", tc.query)
			vm.Expect("docker", "exec", "postgres-id", "psql", "-U", "app-user", "-d", "app-db", "-At", "-c").Return("events|12\n", "", 0)
			out, err := runMaintenance(t, root, Options{Action: "vacuum", FullTable: tc.table, JSON: true}, Deps{Remote: vm})
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Bloat []string `json:"bloat"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Bloat) != 1 || got.Bloat[0] != "events|12" {
				t.Fatalf("vacuum JSON = %q, %v", out.String(), err)
			}
			vm.AssertDone(t)
		})
	}
}

func TestVacuumRejectsMissingEnvironment(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"", "app\n", "\napp\n", "app\n\n", "app\ndb\nextra\n"} {
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			vm.Expect("docker", "ps").Return("postgres-id\n", "", 0)
			vm.Expect("docker", "exec", "postgres-id", "printenv", "POSTGRES_USER", "POSTGRES_DB").Return(data, "", 0)
			_, err := runMaintenance(t, maintenanceProject(t, "", ""), Options{Action: "vacuum"}, Deps{Remote: vm})
			if err == nil || !strings.Contains(err.Error(), "read postgres environment") || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("vacuum with environment %q: %v", data, err)
			}
			vm.AssertDone(t)
		})
	}
}

func TestVacuumFullRefusesOutsideWindow(t *testing.T) {
	t.Parallel()
	root := maintenanceProject(t, "", "Sun 02:00-05:00")
	vm := &remote.Fake{}
	_, err := runMaintenance(t, root, Options{Action: "vacuum", FullTable: "events"}, Deps{Remote: vm, Now: func() time.Time { return maintenanceNow }})
	if err == nil || !strings.Contains(err.Error(), "outside the maintenance window") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("vacuum --full error = %v", err)
	}
	if len(vm.Calls()) != 0 {
		t.Fatalf("vacuum --full reached VM: %#v", vm.Calls())
	}
}

func TestCerts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	server := tlsServer(t, certificate(t, now.Add(-time.Hour), now.Add(6*24*time.Hour)))
	root := maintenanceProject(t, server.URL, "")
	dial := func(ctx context.Context, network, address string, _ *tls.Config) (*tls.Conn, error) {
		dialer := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} // #nosec G402 -- a local test server has a generated certificate.
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return conn.(*tls.Conn), nil
	}
	out, err := runMaintenance(t, root, Options{Action: "certs", JSON: true}, Deps{Remote: &remote.Fake{}, Now: func() time.Time { return now }, DialTLS: dial})
	if ui.ExitCode(err) != ui.ExitError || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("certs exit code = %d, err = %v", ui.ExitCode(err), err)
	}
	var got struct {
		DaysLeft int `json:"days_left"`
	}
	if jsonErr := json.Unmarshal(out.Bytes(), &got); jsonErr != nil || got.DaysLeft != 6 {
		t.Fatalf("certs JSON = %q, %v", out.String(), jsonErr)
	}
}

func TestWindowStatus(t *testing.T) {
	t.Parallel()
	root := maintenanceProject(t, "", "Sun 02:00-05:00")
	out, err := runMaintenance(t, root, Options{Action: "window", JSON: true}, Deps{Remote: &remote.Fake{}, Now: func() time.Time { return maintenanceNow }})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Window    string `json:"window"`
		Inside    bool   `json:"inside"`
		NextStart string `json:"next_start"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Window == "" || got.Inside || got.NextStart != "2026-10-18T02:00:00Z" {
		t.Fatalf("window JSON = %q, %v", out.String(), err)
	}
}

func runMaintenance(t *testing.T, root string, o Options, d Deps) (*bytes.Buffer, error) {
	t.Helper()
	out := &bytes.Buffer{}
	o.Dir = root
	o.Env = "staging"
	o.Out = out
	if d.Now == nil {
		d.Now = func() time.Time { return maintenanceNow }
	}
	return out, Run(context.Background(), o, d)
}

func maintenanceProject(t *testing.T, publicURL, window string) string {
	t.Helper()
	dir := t.TempDir()
	data := "[app]\nname = \"todo\"\nmodule = \"example.com/todo\"\n\n[env.staging]\nhost = \"vm\"\nuser = \"deploy\"\n"
	if publicURL != "" {
		data += "public_url = \"" + publicURL + "\"\n"
	}
	if window != "" {
		data += "\n[maintenance]\nwindow = \"" + window + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "spinup.toml"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func tlsServer(t *testing.T, cert tls.Certificate) *httptest.Server {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("the sandbox does not permit a local TLS listener: %v", err)
	}
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})},
		TLS:      &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func certificate(t *testing.T, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestPruneRetainsReleaseAndContainerImages(t *testing.T) {
	t.Parallel()
	root := maintenanceProject(t, "", "")
	bin := t.TempDir()
	fakeDocker := `#!/bin/sh
set -eu
case "${FAIL_STAGE-}" in
 "") ;;
 "$1"|"$1 ${2-}") exit 1;;
esac
case "$*" in
 'image ls --no-trunc --format {{.Repository}}:{{.Tag}} {{.ID}}')
  printf '%s\n' 'todo-api:r6 sha256:6' 'todo-api:r5 sha256:5' 'todo-api:r4 sha256:4' 'todo-api:alias sha256:4' 'todo-api:r3 sha256:3' 'todo-api:r2 sha256:2' 'todo-api:r1 sha256:1' 'todo-postgres:x sha256:pg' 'todo-api:stopped sha256:stopped' 'other-api:r1 sha256:other';;
 'ps -aq') printf '%s\n' running stopped;;
 'inspect --format {{.Image}} running stopped') printf '%s\n' sha256:6 sha256:pg sha256:stopped;;
 'image inspect --format {{.Id}} todo-api:r6') echo sha256:6;;
 'image inspect --format {{.Id}} todo-api:r5') echo sha256:5;;
 'image inspect --format {{.Id}} registry.example:5000/todo-api@sha256:4') echo sha256:4;;
 'image inspect --format {{.Id}} todo-api:r3') echo sha256:3;;
 'image inspect --format {{.Id}} todo-api:r2') echo sha256:2;;
 *) echo "unexpected docker: $*" >&2; exit 1;;
esac
`
	if err := runexec.WriteExecutable(filepath.Join(bin, "docker"), []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	vm := &remote.Fake{}
	vm.Expect("sh", "-c").Return(`[{"release":"r6","images":[{"ref":"todo-api:r6"}]},{"release":"r5","images":[{"ref":"todo-api:r5"}]},{"release":"r4","images":[{"ref":"registry.example:5000/todo-api@sha256:4"}]},{"release":"r3","images":[{"ref":"todo-api:r3"}]},{"release":"r2","images":[{"ref":"todo-api:r2"}]},{"release":"r1","images":[{"ref":"todo-api:r1"}]}]`, "", 0)
	vm.Expect("sh", "-ec")
	if _, err := runMaintenance(t, root, Options{Action: "prune", Plan: true}, Deps{Remote: vm}); err != nil {
		t.Fatal(err)
	}
	calls := vm.Calls()
	cmd := calls[len(calls)-1]
	for _, stage := range []string{"", "image inspect", "ps", "inspect", "image ls"} {
		t.Run("failure="+stage, func(t *testing.T) {
			t.Parallel()
			result, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: cmd.Argv[0], Args: cmd.Argv[1:], Env: []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "FAIL_STAGE=" + stage}})
			if stage == "" {
				if err != nil || string(result.Stdout) != "todo-api:r1\n" {
					t.Fatalf("prune candidates = %q, stderr = %q, error = %v", result.Stdout, result.Stderr, err)
				}
			} else if err == nil || len(result.Stdout) != 0 {
				t.Fatalf("failed inspection produced candidates: %q, error = %v", result.Stdout, err)
			}
		})
	}
	vm.AssertDone(t)
}

func TestPruneRemovesOnlySelectedImages(t *testing.T) {
	t.Parallel()
	vm := &remote.Fake{}
	vm.Expect("sh", "-c").Return("[]", "", 0)
	vm.Expect("sh", "-ec").Return("todo-api:old\n", "", 0)
	vm.Expect("docker", "image", "rm", "todo-api:old")
	vm.Expect("docker", "builder", "prune", "-f")
	_, err := runMaintenance(t, maintenanceProject(t, "", ""), Options{Action: "prune"}, Deps{Remote: vm})
	if err != nil {
		t.Fatal(err)
	}
	vm.AssertDone(t)
}
