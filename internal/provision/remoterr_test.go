package provision

import (
	"context"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestRunKeepsTheStdoutOfAFailedCommand(t *testing.T) {
	fake := &remote.Fake{}
	fake.Expect("sudo", "-n", hostKeyTool, "next").Return("/etc/ssh/ssh_host_ed25519_key.next already exists.\nOverwrite (y/n)? ", "", 1)
	r := &run{base: base{}, runner: fake}
	_, err := r.run(context.Background(), "sudo", "-n", hostKeyTool, "next")
	if err == nil || !strings.Contains(err.Error(), "stdout: /etc/ssh/ssh_host_ed25519_key.next already exists.") {
		t.Fatalf("err = %v, want the stdout of the command", err)
	}
	if n := strings.Count(err.Error(), "exit code 1"); n != 1 {
		t.Fatalf("the exit code shows %d times in %q", n, err)
	}
}
