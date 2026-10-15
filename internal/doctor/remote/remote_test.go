package remote

import (
	"context"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/project"
	sshremote "github.com/stopyellingatme/spinup/internal/remote"
)

func TestChecks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(context.Context, *doctor.Context) doctor.Result
		set  func(*sshremote.Fake)
		want doctor.Status
	}{
		{"disk", disk, func(f *sshremote.Fake) {
			f.Expect("df", "-P", "/").Return("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 100 10 90 10% /\n", "", 0)
			f.Expect("df", "-P", "/var/lib/docker").Return("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 100 10 90 10% /var/lib/docker\n", "", 0)
		}, doctor.OK},
		{"docker", docker, func(f *sshremote.Fake) {
			f.Expect("docker", "info").Return("ok", "", 0)
			f.Expect("docker", "compose", "--project-name", "todo").Return("api\npostgres", "", 0)
		}, doctor.OK},
		{"deploy lock", deployLock, func(f *sshremote.Fake) { f.Expect("cat", "/opt/todo/deploy.lock/owner.json").Return("", "missing", 1) }, doctor.OK},
		{"unattended upgrades", unattendedUpgrades, func(f *sshremote.Fake) {
			f.Expect("systemctl", "is-enabled", "unattended-upgrades").Return("enabled\n", "", 0)
		}, doctor.OK},
		{"clock", clock, func(f *sshremote.Fake) { f.Expect("timedatectl", "show").Return("yes\n", "", 0) }, doctor.OK},
		{"secrets", secretsDir, func(f *sshremote.Fake) {
			f.Expect("stat", "-c", "%a:%U", "/opt/todo/secrets").Return("700:deploy\n", "", 0)
			f.Expect("find", "/opt/todo/secrets").Return("600:deploy\n", "", 0)
		}, doctor.OK},
		{"backup disabled", backupAge, func(f *sshremote.Fake) {}, doctor.OK},
		{"certificate skipped", certs, func(f *sshremote.Fake) {}, doctor.Skip},
		{"monitoring disabled", monitoring, func(f *sshremote.Fake) {}, doctor.Skip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &sshremote.Fake{}
			tc.set(fake)
			result := tc.run(context.Background(), checkContext(fake))
			if result.Status != tc.want {
				t.Fatalf("status = %s, want %s: %+v", result.Status, tc.want, result)
			}
			fake.AssertDone(t)
		})
	}
}

func TestChecksReportWarningsAndFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(context.Context, *doctor.Context) doctor.Result
		set  func(*sshremote.Fake)
		want doctor.Status
	}{
		{"disk warning", disk, func(f *sshremote.Fake) { f.Expect("df", "-P", "/").Return("x\n/dev/x 100 85 15 85% /\n", "", 0) }, doctor.Warn},
		{"docker failure", docker, func(f *sshremote.Fake) { f.Expect("docker", "info").Return("", "down", 1) }, doctor.Fail},
		{"clock warning", clock, func(f *sshremote.Fake) { f.Expect("timedatectl", "show").Return("no\n", "", 0) }, doctor.Warn},
		{"clock skip", clock, func(f *sshremote.Fake) { f.Expect("timedatectl", "show").Return("", "missing", 1) }, doctor.Skip},
		{"secrets failure", secretsDir, func(f *sshremote.Fake) {
			f.Expect("stat", "-c", "%a:%U", "/opt/todo/secrets").Return("755:deploy\n", "", 0)
		}, doctor.Fail},
		{"unattended warning", unattendedUpgrades, func(f *sshremote.Fake) {
			f.Expect("systemctl", "is-enabled", "unattended-upgrades").Return("disabled\n", "", 0)
		}, doctor.Warn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &sshremote.Fake{}
			tc.set(fake)
			result := tc.run(context.Background(), checkContext(fake))
			if result.Status != tc.want || ((tc.want == doctor.Warn || tc.want == doctor.Fail) && result.Try == "") {
				t.Fatalf("result = %+v, want %s with Try", result, tc.want)
			}
			fake.AssertDone(t)
		})
	}
}

func checkContext(r sshremote.Runner) *doctor.Context {
	config := &project.Config{}
	config.App.Name = "todo"
	config.Monitoring.Profile = "none"
	return &doctor.Context{Env: "production", Deps: map[string]any{
		"remote": r, "config": config, "environment": project.Env{Name: "production", User: "deploy"},
	}}
}
