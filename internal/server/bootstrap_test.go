package server

import (
	"context"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestBootstrapHost(t *testing.T) {
	t.Parallel()
	for _, os := range []string{"debian", "ubuntu"} {
		t.Run(os, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("cat", "/etc/os-release").Return("ID="+os+"\n", "", 0)
			f.Expect("sudo", "-n", "true").Return("", "", 0)
			f.Expect("sh", "-ec").Return("192.0.2.1 54321 192.0.2.2 22\n", "", 0)
			steps := bootstrapSteps("todo", "ssh-ed25519 AAAA operator")
			for range steps {
				f.Expect("sudo", "-n", "sh", "-ec").Return("", "", 1)
				f.Expect("sudo", "-n", "sh", "-ec").Return("", "", 0)
			}
			reports, err := BootstrapHost(context.Background(), f, "todo", "ssh-ed25519 AAAA operator", false)
			if err != nil {
				t.Fatal(err)
			}
			if len(reports) != len(steps) {
				t.Fatal(reports)
			}
			for _, report := range reports {
				if !report.Changed {
					t.Fatal(report)
				}
			}
			f.Expect("cat", "/etc/os-release").Return("ID=\""+os+"\"\n", "", 0)
			f.Expect("sudo", "-n", "true").Return("", "", 0)
			f.Expect("sh", "-ec").Return("192.0.2.1 54321 192.0.2.2 22\n", "", 0)
			for range steps {
				f.Expect("sudo", "-n", "sh", "-ec").Return("", "", 0)
			}
			reports, err = BootstrapHost(context.Background(), f, "todo", "ssh-ed25519 AAAA operator", false)
			if err != nil {
				t.Fatal(err)
			}
			for _, report := range reports {
				if report.Changed {
					t.Fatal(report)
				}
			}
			f.AssertDone(t)
		})
	}
}

func TestBootstrapPlanAndOS(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	f.Expect("cat", "/etc/os-release").Return("ID=ubuntu\n", "", 0)
	f.Expect("sudo", "-n", "true").Return("", "", 0)
	f.Expect("sh", "-ec").Return("192.0.2.1 54321 192.0.2.2 22\n", "", 0)
	for range bootstrapSteps("todo", "key") {
		f.Expect("sudo", "-n", "sh", "-ec").Return("", "", 1)
	}
	reports, err := BootstrapHost(context.Background(), f, "todo", "key", true)
	if err != nil || len(reports) == 0 {
		t.Fatalf("%v, %v", reports, err)
	}
	for _, report := range reports {
		if !report.Changed || !report.Planned {
			t.Fatal(report)
		}
	}
	f.AssertDone(t)
	unsupported := &remote.Fake{}
	unsupported.Expect("cat", "/etc/os-release").Return("ID=alpine\nID_LIKE=debian\n", "", 0)
	if _, err := BootstrapHost(context.Background(), unsupported, "todo", "key", false); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatal(err)
	}
	if len(unsupported.Calls()) != 1 {
		t.Fatal("unsupported host changed")
	}
}

func TestBootstrapPreservesConnectionPort(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	f.Expect("cat", "/etc/os-release").Return("ID=ubuntu\n", "", 0)
	f.Expect("sudo", "-n", "true").Return("", "", 0)
	f.Expect("sh", "-ec").Return("192.0.2.1 54321 192.0.2.2 2222\n", "", 0)
	steps := bootstrapSteps("todo", "key")
	for range steps {
		f.Expect("sudo", "-n", "sh", "-ec").Return("", "", 1)
		f.Expect("sudo", "-n", "sh", "-ec").Return("", "", 0)
	}
	if _, err := BootstrapHost(context.Background(), f, "todo", "key", false); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)
	for i, step := range steps {
		if step.name != "firewall" {
			continue
		}
		for _, call := range f.Calls()[3+2*i : 3+2*i+2] {
			if !strings.Contains(call.Argv[4], "connection_port='2222'") {
				t.Fatalf("firewall script lost connection port: %s", call.Argv[4])
			}
		}
	}
}

func TestBootstrapRejectsUnknownConnectionPort(t *testing.T) {
	t.Parallel()
	for _, connection := range []string{"", "192.0.2.1 54321 192.0.2.2", "192.0.2.1 54321 192.0.2.2 invalid", "192.0.2.1 54321 192.0.2.2 0", "192.0.2.1 54321 192.0.2.2 65536"} {
		t.Run(connection, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("cat", "/etc/os-release").Return("ID=ubuntu\n", "", 0)
			f.Expect("sudo", "-n", "true").Return("", "", 0)
			f.Expect("sh", "-ec").Return(connection, "", 0)
			if _, err := BootstrapHost(context.Background(), f, "todo", "key", false); err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("invalid connection accepted: %v", err)
			}
			f.AssertDone(t)
		})
	}
}
