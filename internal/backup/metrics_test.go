package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestRecordMetrics(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name           string
		sample         Sample
		existing, want string
	}{
		{"backup", Sample{Kind: Diff, Success: time.Unix(123, 0), Size: 456}, "# HELP backup_last_size_bytes Size\n# TYPE backup_last_size_bytes gauge\nother 9\nbackup_last_success_timestamp_seconds{kind=\"full\"} 12\nbackup_last_size_bytes{kind=\"diff\"} 1\n", "# HELP backup_last_size_bytes Size\n# TYPE backup_last_size_bytes gauge\nbackup_last_size_bytes{kind=\"diff\"} 456\nbackup_last_success_timestamp_seconds{kind=\"diff\"} 123\nbackup_last_success_timestamp_seconds{kind=\"full\"} 12\nother 9\n"},
		{"verify failure", Sample{Verified: time.Unix(789, 0), RTO: 1500 * time.Millisecond}, "backup_last_verify_ok 1\nbackup_last_verify_timestamp_seconds 1\nbackup_last_verify_rto_seconds 3\n", "backup_last_verify_ok 0\nbackup_last_verify_rto_seconds 1.5\nbackup_last_verify_timestamp_seconds 789\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("mkdir", "-p", "/opt/todo/backups/metrics")
			f.Expect("chmod", "0755", "/opt/todo/backups/metrics")
			f.Expect("flock", "/opt/todo/backups/metrics/.lock", "sh", "-ec", metricsMergeScript)
			if err := RecordMetrics(context.Background(), f, "todo", tt.sample); err != nil {
				t.Fatal(err)
			}
			f.AssertDone(t)
			command := f.Calls()[2]
			dir := t.TempDir()
			metrics := filepath.Join(dir, "backup.prom")
			if err := os.WriteFile(metrics, []byte(tt.existing), 0644); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, "bin")
			testFlock(t, bin)
			command.Argv[1] = filepath.Join(dir, ".lock")
			command.Argv[len(command.Argv)-1] = metrics
			if _, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: command.Argv[0], Args: command.Argv[1:], Stdin: command.Stdin, Env: []string{"PATH=" + bin + ":" + os.Getenv("PATH")}}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(metrics)
			if err != nil || string(data) != tt.want {
				t.Fatalf("metrics = %s, %v", data, err)
			}
			info, err := os.Stat(metrics)
			if err != nil || info.Mode().Perm() != 0644 {
				t.Fatalf("metrics mode: %v, %v", info, err)
			}
			if len(f.Uploads()) != 0 {
				t.Fatal("metrics used an unlocked upload")
			}

		})
	}
}

func TestMetricsDirectoryFailure(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	f.Expect("mkdir", "-p").Fail(errors.New("permission denied"))
	if err := RecordMetrics(context.Background(), f, "todo", Sample{Kind: Full, Success: time.Unix(1, 0)}); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("error = %v", err)
	}
	if len(f.Uploads()) != 0 {
		t.Fatal("overwrote metrics after read failure")
	}
	f.AssertDone(t)
}

func expectMetrics(f *remote.Fake) {
	f.Expect("mkdir", "-p", "/opt/todo/backups/metrics")
	f.Expect("chmod", "0755", "/opt/todo/backups/metrics")
	f.Expect("flock", "/opt/todo/backups/metrics/.lock", "sh", "-ec", metricsMergeScript)
}

func TestNowMetricsWarning(t *testing.T) {
	t.Parallel()
	dir := newProject(t, true, "", nil)
	f := &remote.Fake{}
	f.ExpectUpload(RemoteConfigPath("todo"))
	f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
	f.Expect(Command("todo", "--type=diff", "backup")...)
	f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
	f.Expect("mkdir", "-p").Fail(errors.New("no disk space"))
	var warnings strings.Builder
	if _, err := Now(context.Background(), Options{Dir: dir, Env: "staging", Remote: f, ErrOut: &warnings}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warnings.String(), "warning: backup succeeded") {
		t.Fatalf("warnings = %q", warnings.String())
	}
	f.AssertDone(t)
}

func TestMetricsBindDirectoryOwnership(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	f.Expect("mkdir", "-p", "/opt/todo/backups/metrics")
	f.Expect("chmod", "0755", "/opt/todo/backups/metrics").Return("", "operation not permitted", 1)
	f.Expect("sudo", "-n", "chown", "--reference=/opt/todo/backups", "/opt/todo/backups/metrics")
	f.Expect("chmod", "0755", "/opt/todo/backups/metrics")
	f.Expect("flock", "/opt/todo/backups/metrics/.lock", "sh", "-ec", metricsMergeScript)
	if err := RecordMetrics(context.Background(), f, "todo", Sample{Kind: Full, Success: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)
}

func TestMetricsUpdateStdin(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	expectMetrics(f)
	if err := RecordMetrics(context.Background(), f, "todo", Sample{Kind: Full, Success: time.Unix(123, 0)}); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)
	data, err := io.ReadAll(f.Calls()[2].Stdin)
	if err != nil || string(data) != "backup_last_success_timestamp_seconds{kind=\"full\"} 123\n" {
		t.Fatalf("updates = %s, %v", data, err)
	}
}
