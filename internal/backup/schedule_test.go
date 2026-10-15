package backup

import (
	"context"
	"os"
	"path/filepath"
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
				if mode == "install" || mode == "remove" {
					if mode == "install" {
						expectCalendars(f)
					}
					for _, argv := range schedulePrivileges("todo", mode == "remove") {
						f.Expect(append([]string{"sudo", "-n", "-ll", "--"}, argv...)...).Return("Sudoers entry:\n    RunAsUsers: root\n    Options: !authenticate\n", "", 0)
					}
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
					for _, timer := range scheduleTimers("todo") {
						f.Expect("sudo", "-n", "systemctl", "enable", "--now", timer)
					}
					f.Expect("systemctl", "list-timers", "--all", "--no-pager").Return("timers installed\n", "", 0)
				}
				if mode == "remove" {
					f.Expect("sh", "-ec", scheduleRemoveScript)
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

func TestScheduleDefaults(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ backend, retention string }{{"pgdump", "7"}, {"pgbackrest", "4"}} {
		t.Run(tt.backend, func(t *testing.T) {
			t.Parallel()
			c := &project.Config{}
			c.App.Name = "todo"
			c.Backups.Backend = tt.backend
			files, err := scheduleFiles(c, project.Env{})
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				if file.Path == "deploy/backups/run.sh" && !strings.Contains(string(file.Data), "retention='"+tt.retention+"'") {
					t.Fatalf("wrong retention for %s", tt.backend)
				}
				if strings.HasSuffix(file.Path, "-diff.timer") && !strings.Contains(string(file.Data), "OnCalendar=Mon..Sat *-*-* 03:00:00") {
					t.Fatal("default diff includes Sunday")
				}
			}
		})
	}
}

func TestScheduleWhitespaceCalendar(t *testing.T) {
	t.Parallel()
	c := &project.Config{}
	c.App.Name = "todo"
	c.Backups.ScheduleFull = " \t "
	if _, err := scheduleFiles(c, project.Env{}); err == nil {
		t.Fatal("accepted whitespace calendar")
	}
}

func expectCalendars(f *remote.Fake) {
	for _, calendar := range []string{"Sun *-*-* 03:00:00", "Mon..Sat *-*-* 03:00:00", "Sat *-*-* 04:00:00"} {
		f.Expect("systemd-analyze", "calendar", "--", calendar)
	}
}

func TestSchedulePreflight(t *testing.T) {
	t.Parallel()
	for _, remove := range []bool{false, true} {
		for denied, command := range schedulePrivileges("todo", remove) {
			t.Run(strings.Join(command, " "), func(t *testing.T) {
				t.Parallel()
				dir := newProject(t, true, "", nil)
				f := &remote.Fake{}
				f.Expect("sh", "-ec", systemdProbe)
				if !remove {
					expectCalendars(f)
				}
				for i, argv := range schedulePrivileges("todo", remove) {
					expectation := f.Expect(append([]string{"sudo", "-n", "-ll", "--"}, argv...)...).Return("Sudoers entry:\n    RunAsUsers: root\n    Options: !authenticate\n", "", 0)
					if i == denied {
						expectation.Return("", "password required", 1)
						break
					}
				}
				err := Schedule(context.Background(), ScheduleOptions{Options: Options{Dir: dir, Env: "staging", Remote: f}, Remove: remove})
				hint := "Try: re-run spinup server bootstrap"
				if remove && command[0] == "rm" {
					hint = "Try: re-run spinup server bootstrap for this environment, which permits rm -f"
				}
				if err == nil || !strings.Contains(err.Error(), hint) {
					t.Fatalf("error = %v", err)
				}
				if len(f.Uploads()) != 0 {
					t.Fatal("uploaded files before sudo check")
				}
				f.AssertDone(t)
			})
		}
	}
}

func TestScheduleInvalidCalendarBeforeUpload(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"schedule_full", "schedule_diff", "schedule_verify"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			dir := newProject(t, true, "", nil)
			file, err := os.OpenFile(filepath.Join(dir, "spinup.toml"), os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = file.WriteString("\n[backups]\n" + key + " = \"garbage\"\n")
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			f := &remote.Fake{}
			f.Expect("sh", "-ec", systemdProbe)
			for _, item := range []struct{ key, value string }{{"schedule_full", "Sun *-*-* 03:00:00"}, {"schedule_diff", "Mon..Sat *-*-* 03:00:00"}, {"schedule_verify", "Sat *-*-* 04:00:00"}} {
				if item.key == key {
					f.Expect("systemd-analyze", "calendar", "--", "garbage").Return("", "Invalid calendar specification", 1)
					break
				}
				f.Expect("systemd-analyze", "calendar", "--", item.value)
			}
			err = Schedule(context.Background(), ScheduleOptions{Options: Options{Dir: dir, Env: "staging", Remote: f}})
			if err == nil || !strings.Contains(err.Error(), "Try: correct [backups]") {
				t.Fatalf("error = %v", err)
			}
			if len(f.Uploads()) != 0 {
				t.Fatal("uploaded invalid calendar")
			}
			f.AssertDone(t)
		})
	}
}

func TestScheduleRejectsPasswordRule(t *testing.T) {
	t.Parallel()
	dir := newProject(t, true, "", nil)
	f := &remote.Fake{}
	f.Expect("sh", "-ec", systemdProbe)
	expectCalendars(f)
	f.Expect("sudo", "-n", "-ll", "--", "systemctl", "daemon-reload").Return("Sudoers entry:\n    Options: authenticate\n    Commands:\n        /usr/bin/systemctl daemon-reload\n", "", 0)
	err := Schedule(context.Background(), ScheduleOptions{Options: Options{Dir: dir, Env: "staging", Remote: f}})
	if err == nil || !strings.Contains(err.Error(), "Try: re-run spinup server bootstrap") {
		t.Fatalf("error = %v", err)
	}
	if len(f.Uploads()) != 0 {
		t.Fatal("uploaded before passwordless sudo check")
	}
	f.AssertDone(t)
}
