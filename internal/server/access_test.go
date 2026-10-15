package server

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestLogsHost(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	f.Expect("docker", "ps").Return("aabbcc todo-blue\nddeeff other-app\n", "", 0)
	f.Expect("docker", "logs", "--follow", "--since", "10m", "aabbcc").Return("ready\n", "", 0)
	var out bytes.Buffer
	if err := LogsHost(context.Background(), f, "todo", "api", Options{Follow: true, Since: "10m", Out: &out}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ready\n" {
		t.Fatal(out.String())
	}
	f.AssertDone(t)
	calls := f.Calls()
	want := []string{"docker", "ps", "--filter", "label=com.docker.compose.service=api", "--format", `{{.ID}} {{.Label "com.docker.compose.project"}}`}
	if !reflect.DeepEqual(calls[0].Argv, want) {
		t.Fatal(calls[0].Argv)
	}
}

func TestInteractiveAdapter(t *testing.T) {
	t.Parallel()
	f := &runexec.Fake{}
	f.Expect("ssh", "-t").Return("", "", 0)
	f.Expect("ssh", "-O", "exit").Return("", "", 0)
	adapter := shellRunner{runner: f}
	if _, err := adapter.Run(context.Background(), runexec.Cmd{Name: "ssh", Args: []string{"--", "deploy@vm"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Run(context.Background(), runexec.Cmd{Name: "ssh", Args: []string{"-O", "exit", "--", "deploy@vm"}}); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if !calls[0].Interactive || calls[1].Interactive {
		t.Fatal(calls)
	}
	f.AssertDone(t)
}

func TestMemory(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, data string
		status     doctor.Status
		message    string
	}{
		{"large", "MemTotal: 4194304 kB\n", doctor.OK, "4 GiB"},
		{"lite", "MemTotal: 3145728 kB\n", doctor.OK, "lite"},
		{"minimum", "MemTotal: 2097152 kB\n", doctor.OK, "lite"},
		{"small", "MemTotal: 1048576 kB\n", doctor.Warn, "2 GiB"},
		{"invalid", "MemTotal: unavailable\n", doctor.Fail, "RAM"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("cat", "/proc/meminfo").Return(tt.data, "", 0)
			result := memoryCheck(context.Background(), &doctor.Context{Deps: map[string]any{"remote": f}})
			if result.Status != tt.status || !strings.Contains(result.Message, tt.message) {
				t.Fatal(result)
			}
			f.AssertDone(t)
		})
	}
	if result := memoryCheck(context.Background(), &doctor.Context{}); result.Status != doctor.Skip {
		t.Fatal(result)
	}
	found := false
	for _, check := range doctor.Checks(doctor.Remote, false) {
		if check.ID == "vm-memory" {
			found = true
		}
	}
	if !found {
		t.Fatal("vm-memory not registered")
	}
}

func TestLogsReplicas(t *testing.T) {
	t.Parallel()
	for _, follow := range []bool{false, true} {
		t.Run(map[bool]string{false: "snapshot", true: "follow"}[follow], func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("docker", "ps").Return("aabbcc todo-blue\naabbee todo-green\nabcdef other\n", "", 0)
			f.Expect("docker", "logs").Return("replica\n", "warning\n", 0)
			f.Expect("docker", "logs").Return("replica\n", "warning\n", 0)
			var combined bytes.Buffer
			if err := LogsHost(context.Background(), f, "todo", "api", Options{Follow: follow, Out: &combined, ErrOut: &combined}); err != nil {
				t.Fatal(err)
			}
			if strings.Count(combined.String(), "replica") != 2 || strings.Count(combined.String(), "warning") != 2 {
				t.Fatal(combined.String())
			}
			seen := map[string]bool{}
			for _, call := range f.Calls()[1:] {
				seen[call.Argv[len(call.Argv)-1]] = true
			}
			if !seen["aabbcc"] || !seen["aabbee"] || len(seen) != 2 {
				t.Fatal(seen)
			}
			f.AssertDone(t)
		})
	}
}

func TestLogsInvalidAndMissingService(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	if err := LogsHost(context.Background(), f, "todo", "api; touch /tmp/injected", Options{}); err == nil || len(f.Calls()) != 0 {
		t.Fatal("invalid service reached SSH", err)
	}
	f.Expect("docker", "ps").Return("", "", 0)
	if err := LogsHost(context.Background(), f, "todo", "api", Options{}); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatal(err)
	}
	f.AssertDone(t)
}
