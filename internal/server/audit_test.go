package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/remote"
)

const safeInspect = `[{"Name":"/todo-api-1","Config":{"User":"1000:1000","Env":["DATABASE_URL_FILE=/run/secrets/DATABASE_URL","TOKEN_FILE=/run/secrets/TOKEN"],"Labels":{"com.docker.compose.service":"api"}},"HostConfig":{"ReadonlyRootfs":true,"CapDrop":["ALL"],"SecurityOpt":["no-new-privileges=true"],"PortBindings":{}},"NetworkSettings":{"Ports":{}}}]`
const safeSSHD = "passwordauthentication no\nkbdinteractiveauthentication no\npermitrootlogin no\npubkeyauthentication yes\nauthenticationmethods publickey\n"

func TestInspectChecks(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, old, value, rule string }{
		{"safe", "", "", ""},
		{"writable", `"ReadonlyRootfs":true`, `"ReadonlyRootfs":false`, "read-only"},
		{"capabilities", `"CapDrop":["ALL"]`, `"CapDrop":[]`, "cap-drop"},
		{"added capability", `"CapDrop":["ALL"]`, `"CapDrop":["ALL"],"CapAdd":["SYS_ADMIN"]`, "cap-drop"},
		{"privileges", `"SecurityOpt":["no-new-privileges=true"]`, `"SecurityOpt":[]`, "no-new-privileges"},
		{"root", `"User":"1000:1000"`, `"User":"0:1000"`, "non-root"},
		{"root name", `"User":"1000:1000"`, `"User":"root"`, "non-root"},
		{"implicit root", `"User":"1000:1000"`, `"User":""`, "non-root"},
		{"root zeros", `"User":"1000:1000"`, `"User":"000"`, "non-root"},
		{"secret", `"TOKEN_FILE=/run/secrets/TOKEN"`, `"TOKEN=do-not-disclose"`, "literal-secret"},
		{"password url", `"DATABASE_URL_FILE=/run/secrets/DATABASE_URL"`, `"DATABASE_URL=postgres://app:do-not-disclose@postgres/app"`, "literal-secret"},
		{"public api", `"PortBindings":{}`, `"PortBindings":{"8080/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}]}`, "public-port"},
		{"ipv6 public", `"PortBindings":{}`, `"PortBindings":{"8080/tcp":[{"HostIp":"::","HostPort":"8080"}]}`, "public-port"},
		{"runtime binding", `"Ports":{}`, `"Ports":{"8080/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}]}`, "public-port"},
		{"postgres local", `"PortBindings":{}`, `"PortBindings":{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"5432"}]}`, "postgres-port"},
		{"host namespace", `"PortBindings":{}`, `"PortBindings":{},"NetworkMode":"host"`, "public-port"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := safeInspect
			if tt.old != "" {
				data = strings.Replace(data, tt.old, tt.value, 1)
			}
			results := InspectChecks([]byte(data))
			failed := map[string]bool{}
			for _, result := range results {
				if result.Status == doctor.Fail.String() {
					failed[result.Check] = true
					if result.Try == "" {
						t.Fatal(result)
					}
				}
			}
			if (tt.rule == "" && len(failed) != 0) || (tt.rule != "" && !failed[tt.rule]) {
				t.Fatalf("results = %+v", results)
			}
			encoded, err := json.Marshal(results)
			if err != nil || strings.Contains(string(encoded), "do-not-disclose") {
				t.Fatalf("report leaks a secret: %s, %v", encoded, err)
			}
		})
	}
}

func TestInspectPorts(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, service, port, ip, hostPort string
		safe                              bool
	}{
		{"private api", "api", "8080/tcp", "127.0.0.1", "8080", true},
		{"IPv6 loopback", "api", "8080/tcp", "::1", "8080", false},
		{"Caddy http", "caddy", "80/tcp", "0.0.0.0", "80", true},
		{"Caddy https", "caddy", "443/tcp", "::", "443", true},
		{"Caddy quic", "caddy", "443/udp", "0.0.0.0", "443", true},
		{"Caddy admin", "caddy", "2019/tcp", "0.0.0.0", "2019", false},
		{"Caddy mapped admin", "caddy", "2019/tcp", "0.0.0.0", "443", false},
		{"api port 80", "api", "80/tcp", "0.0.0.0", "80", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := strings.Replace(safeInspect, `"com.docker.compose.service":"api"`, `"com.docker.compose.service":"`+tt.service+`"`, 1)
			ports := `"PortBindings":{"` + tt.port + `":[{"HostIp":"` + tt.ip + `","HostPort":"` + tt.hostPort + `"}]}`
			results := InspectChecks([]byte(strings.Replace(data, `"PortBindings":{}`, ports, 1)))
			for _, result := range results {
				if result.Check == "public-port" && (result.Status == "ok") != tt.safe {
					t.Fatal(result)
				}
			}
		})
	}
}

func TestAuditHost(t *testing.T) {
	t.Parallel()
	for _, bad := range []bool{false, true} {
		t.Run(map[bool]string{false: "pass", true: "fail"}[bad], func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			sshd, disk, active := safeSSHD, "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/vda 1000 700 300 70% /\n", "active\n"
			if bad {
				sshd = "passwordauthentication yes\npermitrootlogin yes\n"
				disk = "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/vda 1000 850 150 85% /\n"
				active = "inactive\n"
			}
			f.Expect("sudo", "-n", "/usr/sbin/sshd", "-T").Return(sshd, "", 0)
			f.Expect("docker", "ps", "-q").Return("aabbcc\n", "", 0)
			f.Expect("docker", "inspect", "aabbcc").Return(safeInspect, "", 0)
			f.Expect("df", "-Pk", "/opt/todo").Return(disk, "", 0)
			f.Expect("systemctl", "is-active", "unattended-upgrades").Return(active, "", 0)
			results := AuditHost(context.Background(), f, "todo")
			failures := 0
			for _, result := range results {
				if result.Status == "fail" {
					failures++
				}
			}
			if bad && failures != 4 || !bad && failures != 0 {
				t.Fatalf("audit = %+v", results)
			}
			f.AssertDone(t)
		})
	}
}

func TestAuditProbeFailures(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	f.Expect("sudo").Return("", "permission denied", 1)
	f.Expect("docker", "ps", "-q").Return("aabbcc\n", "", 0)
	f.Expect("docker", "inspect").Return("", "do-not-disclose", 1)
	f.Expect("df").Return("", "disk unavailable", 1)
	f.Expect("systemctl").Return("inactive\n", "", 3)
	results := AuditHost(context.Background(), f, "todo")
	if len(results) != 5 {
		t.Fatal(results)
	}
	for _, result := range results {
		if result.Status != "fail" || result.Try == "" || strings.Contains(result.Message, "do-not-disclose") {
			t.Fatal(result)
		}
	}
	f.AssertDone(t)
}

func TestInspectInvalidAndLiteralValues(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"{", "[]", "null", "[{}]", `[ {"Config":{"Env":["TOKEN=do-not-disclose"]}} ]`} {
		results := InspectChecks([]byte(data))
		if len(results) != 1 || results[0].Status != "fail" || strings.Contains(results[0].Message, "do-not-disclose") {
			t.Fatal(results)
		}
	}
	for _, value := range []string{"$TOKEN", "${TOKEN}", "${TOKEN:-default}"} {
		results := InspectChecks([]byte(strings.Replace(safeInspect, "TOKEN_FILE=/run/secrets/TOKEN", "TOKEN="+value, 1)))
		found := false
		for _, r := range results {
			if r.Check == "literal-secret" && r.Status == "fail" {
				found = true
			}
		}
		if !found {
			t.Fatalf("inspect considered %q an interpolation", value)
		}
	}
}

func TestDiskBoundaries(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, data string
		safe       bool
	}{
		{"above threshold", "fs blocks used available pct mounted\n/dev/a 1000 849 151 85% /", true},
		{"at threshold", "fs blocks used available pct mounted\n/dev/a 1000 850 150 85% /", false},
		{"below threshold", "fs blocks used available pct mounted\n/dev/a 1000 851 149 86% /", false},
		{"malformed", "invalid output", false},
		{"invalid counts", "fs blocks used available pct mounted\n/dev/a 1000 nope 300 70% /", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := diskSafe(tt.data); got != tt.safe {
				t.Fatalf("safe=%v", got)
			}
		})
	}
}

func TestGrafanaAuth(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"false", "true", "1"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			data := strings.Replace(safeInspect, `"com.docker.compose.service":"api"`, `"com.docker.compose.service":"grafana"`, 1)
			data = strings.Replace(data, `"TOKEN_FILE=/run/secrets/TOKEN"`, `"GF_AUTH_ANONYMOUS_ENABLED=`+value+`"`, 1)
			found := false
			for _, r := range InspectChecks([]byte(data)) {
				if r.Check == "grafana-auth" {
					found = true
					if (r.Status == "ok") != (value == "false") {
						t.Fatal(r)
					}
				}
			}
			if !found {
				t.Fatal("Grafana auth check missing")
			}
		})
	}
}
