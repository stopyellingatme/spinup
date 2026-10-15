package remote

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestSSHIntegration(t *testing.T) {
	address := os.Getenv("SPINUP_SSH_TEST_HOST")
	if address == "" {
		t.Skip("SPINUP_SSH_TEST_HOST is not set")
	}
	port := 22
	if value := os.Getenv("SPINUP_SSH_TEST_PORT"); value != "" {
		var err error
		port, err = strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	host, err := HostFromEnv(project.Env{Host: address, User: os.Getenv("SPINUP_SSH_TEST_USER"), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := New(host, runexec.OSRunner{}, Options{CacheDir: shortCache(t), AcceptNewHostKey: os.Getenv("SPINUP_SSH_TEST_ACCEPT_NEW_HOST_KEY") == "1"})
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		if err := r.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	result, err := r.Run(ctx, Cmd{Argv: []string{"mktemp", "-d"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := strings.TrimSpace(string(result.Stdout))
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := r.Run(cleanupCtx, Cmd{Argv: []string{"rm", "-rf", "--", dir}}); err != nil {
			t.Error(err)
		}
	}()
	path := dir + "/hostile ' $(false)\nfile"
	content := []byte("secret\x00bytes\n'$(false)")
	if err := r.Upload(ctx, content, path, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = r.Run(ctx, Cmd{Argv: []string{"cat", path}})
	if err != nil || !bytes.Equal(result.Stdout, content) {
		t.Fatalf("roundtrip = %q, %v", result.Stdout, err)
	}
	result, err = r.Run(ctx, Cmd{Argv: []string{"ls", "-l", path}})
	if err != nil || !bytes.HasPrefix(result.Stdout, []byte("-rw-------")) {
		t.Fatalf("mode = %q, %v", result.Stdout, err)
	}
	hostile := "a b'c\n$(false)"
	result, err = r.Run(ctx, Cmd{Argv: []string{"printf", "%s", hostile}})
	if err != nil || string(result.Stdout) != hostile {
		t.Fatalf("quoted command = %q, %v", result.Stdout, err)
	}
}
