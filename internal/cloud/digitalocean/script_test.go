package digitalocean

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean/dofake"
)

func TestRegisterScript(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	f := dofake.New(dofake.Options{ClientID: "client-xyz"})
	base := f.Start()
	defer f.Close()

	cmd := exec.CommandContext(context.Background(), "bash", "../../../scripts/do-register-client.sh")
	cmd.Env = append(cmd.Environ(), "SPINUP_DO_OAUTH_BASE="+base)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "client-xyz" {
		t.Fatalf("stdout = %q, want only the client ID", got)
	}
	if !strings.Contains(stderr.String(), "registration_access_token: reg-") || !strings.Contains(stderr.String(), "password manager") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "reg-") {
		t.Fatal("the registration token must not reach stdout")
	}
}
