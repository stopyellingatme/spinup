package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

// shellTransport executes the final SSH argument through the local POSIX shell.
type shellTransport struct{}

func (shellTransport) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
	return (runexec.OSRunner{}).Run(ctx, runexec.Cmd{Name: "sh", Args: []string{"-c", c.Args[len(c.Args)-1]}, Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr, Timeout: c.Timeout, Redact: c.Redact})
}

func TestShellQuotingAndUpload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := New(Host{Address: "vm"}, shellTransport{}, Options{CacheDir: shortCache(t)})
	for _, value := range []string{"", "spaces and tabs\t", "a'b\"c", "$(false)", "`false`", "line\nnext"} {
		result, err := r.Run(ctx, Cmd{Argv: []string{"printf", "%s", value}})
		if err != nil || string(result.Stdout) != value {
			t.Fatalf("roundtrip %q = %q, %v", value, result.Stdout, err)
		}
	}
	root := t.TempDir()
	path := filepath.Join(root, "a'b $(false)\nfile")
	for _, data := range [][]byte{[]byte("secret\x00first\n"), []byte("replacement ' $(false)")} {
		if err := r.Upload(ctx, data, path, 0640); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("upload = %q, %v", got, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0640 {
			t.Fatalf("upload mode = %v, %v", info, err)
		}
	}
	// An existing directory makes mv fail. The upload must remove its temporary file.
	// A directory must not receive the temporary file as a child.
	blocker := filepath.Join(root, "directory")
	if err := os.Mkdir(blocker, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.Upload(ctx, []byte("data"), blocker, 0600); err == nil {
		t.Fatal("upload to directory succeeded")
	}
	leftovers, err := filepath.Glob(blocker + ".tmp.*")
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files = %v, %v", leftovers, err)
	}
}

func TestFakeScriptAndCopies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := &Fake{}
	sentinel := errors.New("test failure")
	f.Expect("printf", "secret").Return("secret", "secret", 0)
	f.ExpectUpload("/tmp/file").Fail(sentinel)
	f.ExpectClose().Return("", "", 0)
	var out bytes.Buffer
	argv := []string{"printf", "secret"}
	result, err := f.Run(ctx, Cmd{Argv: argv, Stdout: &out, Redact: []string{"secret"}, Stdin: strings.NewReader("input")})
	if err != nil || string(result.Stdout) != "***" || out.String() != "***" {
		t.Fatalf("Run = %#v, %v, %q", result, err, out.String())
	}
	argv[0] = "mutated"
	calls := f.Calls()
	calls[0].Argv[0] = "also mutated"
	if f.Calls()[0].Argv[0] != "printf" {
		t.Fatal("calls share argv storage")
	}
	if data, err := io.ReadAll(f.Calls()[0].Stdin); err != nil || string(data) != "input" {
		t.Fatalf("recorded stdin = %q, %v", data, err)
	}
	data := []byte("data")
	if err := f.Upload(ctx, data, "/tmp/file", 0600); !errors.Is(err, sentinel) {
		t.Fatalf("Upload = %v", err)
	}
	data[0] = 'X'
	uploads := f.Uploads()
	uploads[0].Data[0] = 'Y'
	if !reflect.DeepEqual(f.Uploads(), []UploadCall{{Data: []byte("data"), Path: "/tmp/file", Mode: 0600}}) {
		t.Fatalf("uploads = %#v", f.Uploads())
	}
	if err := f.Close(ctx); err != nil || f.CloseCalls() != 1 {
		t.Fatalf("Close = %v, %d", err, f.CloseCalls())
	}
	f.AssertDone(t)
	if _, err := f.Run(ctx, Cmd{Argv: []string{"unexpected"}}); err == nil {
		t.Fatal("unexpected command accepted")
	}
}

func TestQuotedSecretsAreRedactedFromCommandErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	secret := "private'value"
	f := &runexec.Fake{}
	f.Expect("ssh").Return("", secret, 1)
	r := New(Host{Address: "vm"}, f, Options{CacheDir: shortCache(t)})
	_, err := r.Run(ctx, Cmd{Argv: []string{"false", secret}, Redact: []string{secret}})
	if err == nil {
		t.Fatal("command unexpectedly succeeded")
	}
	// Shell escaping splits the original secret around its embedded quote.
	escaped := strings.ReplaceAll(secret, "'", "'\"'\"'")
	var exitErr *runexec.ExitError
	if !errors.As(err, &exitErr) || strings.Contains(exitErr.Cmd, secret) || strings.Contains(exitErr.Cmd, "private") || strings.Contains(exitErr.Cmd, escaped) || !strings.Contains(exitErr.Cmd, "***") {
		t.Fatalf("command error exposes a quoted secret: %v", err)
	}
}
