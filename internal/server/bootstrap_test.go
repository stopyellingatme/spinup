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
