package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
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

func TestBootstrapBackupSudoers(t *testing.T) {
	t.Parallel()
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0755); err != nil {
				t.Fatal(err)
			}
			// Rejecting validation must leave the active policy intact.
			policy := filepath.Join(dir, "spinup-todo")
			if err := os.WriteFile(policy, []byte("old policy\n"), 0440); err != nil {
				t.Fatal(err)
			}
			validator := `#!/bin/sh
set -eu
[ "$1" = -cf ]
[ "$(cat "$2")" != 'old policy' ]
[ "$INVALID" = false ]
`
			if err := os.WriteFile(filepath.Join(bin, "visudo"), []byte(validator), 0755); err != nil {
				t.Fatal(err)
			}
			var script string
			for _, step := range bootstrapSteps("todo", "key") {
				if step.name == "backup-sudo" {
					script = step.apply
				}
			}
			if script == "" {
				t.Fatal("missing backup sudo step")
			}
			script = strings.ReplaceAll(script, "/etc/sudoers.d", dir)
			_, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{"-ec", script}, Env: []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "INVALID=" + fmt.Sprint(invalid)}})
			if (err != nil) != invalid {
				t.Fatalf("apply: %v", err)
			}
			data, err := os.ReadFile(policy)
			if err != nil {
				t.Fatal(err)
			}
			want := backupSudoers("todo")
			if invalid {
				want = "old policy\n"
			}
			if string(data) != want {
				t.Fatalf("policy = %s", data)
			}
			info, err := os.Stat(policy)
			if err != nil || info.Mode().Perm() != 0440 {
				t.Fatalf("policy mode = %v, %v", info, err)
			}
			// Check the generated policy with the real parser when available.
			if !invalid {
				if visudo, err := runexec.LookPath("visudo"); err == nil {
					if _, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: visudo, Args: []string{"-cf", policy}}); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestCloudInitBackupSudoers(t *testing.T) {
	t.Parallel()
	// Both bootstrap paths must grant exactly the same commands.
	data, err := os.ReadFile("../templates/packs/deploy/files/deploy/cloud-init/user-data.yaml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), "[[ .app_name ]]", "todo")
	if !strings.Contains(text, strings.TrimSpace(backupSudoers("todo"))) {
		t.Fatal("cloud-init policy differs from bootstrap")
	}
	for _, want := range []string{"permissions: \"0440\"", "visudo -cf /run/spinup-todo-sudoers && mv -f /run/spinup-todo-sudoers /etc/sudoers.d/spinup-todo"} {
		if !strings.Contains(text, want) {
			t.Fatalf("cloud-init missing %q", want)
		}
	}
	if strings.Contains(backupSudoers("todo"), "*") {
		t.Fatal("backup sudo permissions use a wildcard")
	}
}

func TestBackupSudoersPermitsScheduleRemove(t *testing.T) {
	t.Parallel()
	got := backupSudoers("todo")
	want := "/usr/bin/rm -f /etc/systemd/system/spinup-todo-backup-full.timer /etc/systemd/system/spinup-todo-backup-full.service /etc/systemd/system/spinup-todo-backup-diff.timer /etc/systemd/system/spinup-todo-backup-diff.service /etc/systemd/system/spinup-todo-backup-verify.timer /etc/systemd/system/spinup-todo-backup-verify.service\n"
	if !strings.HasSuffix(got, ", "+want) {
		t.Fatalf("sudoers = %q, want the rm rule of backup schedule --remove last", got)
	}
}
