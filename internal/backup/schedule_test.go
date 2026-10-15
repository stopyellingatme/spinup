package backup

import (
	"context"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestSchedule(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"install", "remove", "plan", "no-systemd"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			dir := newProject(t, true, "", nil)
			f := &remote.Fake{}
			var out strings.Builder
			if mode != "plan" {
				probe := f.Expect("sh", "-ec", systemdProbe)
				if mode == "no-systemd" {
					probe.Return("", "", 1)
				}
				if mode == "install" {
					f.Expect("mkdir", "-p")
					f.Expect("chmod", "0755")
					f.ExpectUpload("/opt/todo/backups/run.sh")
					f.ExpectUpload("/opt/todo/backups/backend")
					f.ExpectUpload("/opt/todo/backups/pgbackrest.conf")
					for _, kind := range []string{"full", "diff", "verify"} {
						for _, ext := range []string{"service", "timer"} {
							name := "spinup-todo-backup-" + kind + "." + ext
							f.ExpectUpload("/opt/todo/backups/" + name)
							f.Expect("sudo", "-n", "install", "-m", "0644", "/opt/todo/backups/"+name, "/etc/systemd/system/"+name)
							f.Expect("rm", "-f", "/opt/todo/backups/"+name)
						}
					}
					f.Expect("sudo", "-n", "systemctl", "daemon-reload")
					f.Expect("sudo", "-n", "systemctl", "enable", "--now", "spinup-todo-backup-full.timer", "spinup-todo-backup-diff.timer", "spinup-todo-backup-verify.timer")
					f.Expect("systemctl", "list-timers", "--all", "--no-pager").Return("timers installed\n", "", 0)
				}
				if mode == "remove" {
					f.Expect("sudo", "-n", "systemctl", "disable", "--now")
					f.Expect("sudo", "-n", "rm", "-f")
					f.Expect("sudo", "-n", "systemctl", "daemon-reload")
				}
			}
			err := Schedule(context.Background(), ScheduleOptions{Options: Options{Dir: dir, Env: "staging", Remote: f, Out: &out}, Plan: mode == "plan", Remove: mode == "remove"})
			if mode == "no-systemd" {
				if err == nil || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			f.AssertDone(t)
			if mode == "plan" {
				if len(f.Calls()) != 0 || len(f.Uploads()) != 0 || !strings.Contains(out.String(), "OnCalendar=Sun *-*-* 03:00:00") {
					t.Fatalf("plan = %q, calls = %v", out.String(), f.Calls())
				}
			}
			if mode == "install" {
				uploads := f.Uploads()
				if uploads[0].Mode != 0755 || !strings.Contains(string(uploads[3].Data), "User=deploy\n") {
					t.Fatalf("uploads = %+v", uploads)
				}
			}
		})
	}
}

func TestScheduleConfiguration(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, full, user, backend, retention string
		invalid                              bool
	}{
		{name: "custom", full: "Mon *-*-* 05:00:00 UTC", user: "operator", backend: "pgdump", retention: "9"},
		{name: "calendar injection", full: "daily\nUser=root", invalid: true},
		{name: "user injection", user: "root\nExecStart=bad", invalid: true},
		{name: "unknown backend", backend: "other", invalid: true},
		{name: "invalid retention", retention: "bad", invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &project.Config{}
			c.App.Name = "todo"
			c.Backups.ScheduleFull = tt.full
			c.Backups.Backend = tt.backend
			c.Backups.Retention = tt.retention
			files, err := scheduleFiles(c, project.Env{User: tt.user})
			if tt.invalid {
				if err == nil || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			for _, f := range files {
				text.Write(f.Data)
			}
			for _, want := range []string{"OnCalendar=" + tt.full, "User=" + tt.user, "retention='9'"} {
				if !strings.Contains(text.String(), want) {
					t.Errorf("render missing %q", want)
				}
			}
		})
	}
}

func TestScheduleStatus(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	var out strings.Builder
	f.Expect("sh", "-ec").Return("timers\nbackup metrics:\nbackup_last_verify_ok 1\n", "", 0)
	if err := ShowScheduleStatus(context.Background(), f, "todo", &out); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)
	if !strings.Contains(out.String(), "backup_last_verify_ok 1") {
		t.Fatalf("status = %q", out.String())
	}
	script := f.Calls()[0].Argv[2]
	for _, want := range []string{"/etc/systemd/system/spinup-todo-backup-full.timer", "systemctl list-timers --all --no-pager", "/opt/todo/backups/metrics/backup.prom"} {
		if !strings.Contains(script, want) {
			t.Errorf("probe missing %q", want)
		}
	}
}
