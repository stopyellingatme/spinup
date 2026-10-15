package remote

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestHostFromEnv(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		env  project.Env
		want Host
		bad  bool
	}{
		{name: "defaults", env: project.Env{Name: "stage", Host: "vm"}, want: Host{Name: "stage", Address: "vm", Port: 22}},
		{name: "explicit", env: project.Env{Host: "::1", User: "deploy", Port: 2222}, want: Host{Address: "::1", User: "deploy", Port: 2222}},
		{name: "missing", bad: true}, {name: "option", env: project.Env{Host: "-oProxyCommand=bad"}, bad: true},
		{name: "port", env: project.Env{Host: "vm", Port: 65536}, bad: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := HostFromEnv(tt.env)
			if (err != nil) != tt.bad || !tt.bad && got != tt.want {
				t.Fatalf("HostFromEnv = %#v, %v", got, err)
			}
		})
	}
}

func TestSSHArguments(t *testing.T) {
	t.Parallel()
	for _, accept := range []bool{false, true} {
		t.Run(map[bool]string{false: "strict", true: "accept"}[accept], func(t *testing.T) {
			t.Parallel()
			f := &runexec.Fake{}
			f.Expect("ssh").Return("ok", "warning", 0)
			f.Expect("ssh").Return("", "", 0)
			cache := shortCache(t)
			r := New(Host{Address: "vm", User: "deploy", Port: 2222}, f, Options{CacheDir: cache, AcceptNewHostKey: accept})
			var out, errout bytes.Buffer
			input := strings.NewReader("input")
			secret := "a'b"
			result, err := r.Run(context.Background(), Cmd{Argv: []string{"printf", "%s", "a b", secret, "$(touch /tmp/bad)", "line\nnext", ""}, Stdin: input, Stdout: &out, Stderr: &errout, Timeout: time.Minute, Redact: []string{secret}})
			if err != nil || string(result.Stdout) != "ok" || out.String() != "ok" || errout.String() != "warning" {
				t.Fatalf("Run = %#v, %v", result, err)
			}
			calls := f.Calls()
			c := calls[0]
			cp := "ControlPath=" + filepath.Join(cache, "spinup", "ssh", "%C")
			want := []string{"-o", "ControlMaster=auto", "-o", cp, "-o", "ControlPersist=60s", "-p", "2222", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}
			if accept {
				want = append(want, "-o", "StrictHostKeyChecking=accept-new")
			}
			want = append(want, "--", "deploy@vm", `'printf' '%s' 'a b' 'a'"'"'b' '$(touch /tmp/bad)' 'line`+"\n"+`next' ''`)
			if !reflect.DeepEqual(c.Args, want) {
				t.Fatalf("args = %#v, want %#v", c.Args, want)
			}
			if c.Stdin != input || c.Timeout != time.Minute || !reflect.DeepEqual(c.Redact, []string{secret, strings.ReplaceAll(secret, "'", "'\"'\"'")}) {
				t.Fatalf("forwarded command = %#v", c)
			}
			path := strings.TrimPrefix(cp, "ControlPath=")
			if !strings.HasPrefix(path, filepath.Join(cache, "spinup", "ssh")+string(filepath.Separator)) || len(path)-2+40+17 >= 104 {
				t.Fatalf("control path = %q", path)
			}
			info, e := os.Stat(filepath.Dir(path))
			if e != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("cache mode: %v, %v", info, e)
			}
			if err := r.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			last := f.Calls()[1].Args
			if !reflect.DeepEqual(last, append(append([]string(nil), want[:len(want)-3]...), "-O", "exit", "--", "deploy@vm")) {
				t.Fatalf("close args: %#v", last)
			}
			f.AssertDone(t)
		})
	}
}

func TestUploadUsesStdin(t *testing.T) {
	t.Parallel()
	f := &runexec.Fake{}
	f.Expect("ssh").Return("", "", 0)
	r := New(Host{Address: "vm"}, f, Options{CacheDir: shortCache(t)})
	data := []byte("secret 'value'\n$(bad)")
	if err := r.Upload(context.Background(), data, "/tmp/a'b", 0600); err != nil {
		t.Fatal(err)
	}
	c := f.Calls()[0]
	all := strings.Join(c.Args, " ")
	if strings.Contains(all, string(data)) || !strings.Contains(all, "mktemp") || !strings.Contains(all, "chmod") || !strings.Contains(all, "mv") {
		t.Fatalf("upload argv: %s", all)
	}
	got, err := io.ReadAll(c.Stdin)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stdin = %q, %v", got, err)
	}
	f.AssertDone(t)
}

func shortCache(t *testing.T) string {
	t.Helper()
	// t.TempDir paths on macOS can exceed the SSH socket limit.
	dir, err := os.MkdirTemp("/tmp", "su-ssh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestInvalidCommandsAndCache(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		host  Host
		cache string
		cmd   Cmd
	}{
		{name: "empty argv", host: Host{Address: "vm"}},
		{name: "NUL argv", host: Host{Address: "vm"}, cmd: Cmd{Argv: []string{"printf", "bad\x00arg"}}},
		{name: "invalid host", host: Host{Address: "-oProxyCommand=bad"}, cmd: Cmd{Argv: []string{"true"}}},
		{name: "temporary socket suffix", host: Host{Address: "vm"}, cache: "/" + strings.Repeat("a", 64), cmd: Cmd{Argv: []string{"true"}}},
		{name: "long cache", host: Host{Address: "vm"}, cache: "/" + strings.Repeat("a", 104), cmd: Cmd{Argv: []string{"true"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cache := tt.cache
			if cache == "" {
				cache = shortCache(t)
			}
			f := &runexec.Fake{}
			r := New(tt.host, f, Options{CacheDir: cache})
			if _, err := r.Run(context.Background(), tt.cmd); err == nil {
				t.Fatal("invalid command accepted")
			}
			if len(f.Calls()) != 0 {
				t.Fatal("invalid command reached SSH")
			}
		})
	}
}

func TestUploadFailureAndInvalidPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := &runexec.Fake{}
	r := New(Host{Address: "vm"}, f, Options{CacheDir: shortCache(t)})
	for _, path := range []string{"", "bad\x00path"} {
		if err := r.Upload(ctx, []byte("secret"), path, 0600); err == nil {
			t.Fatalf("accepted path %q", path)
		}
	}
	f.Expect("ssh").Return("", "connection failed", 255)
	if err := r.Upload(ctx, []byte("secret"), "/tmp/file", 0600); err == nil {
		t.Fatal("upload failure was ignored")
	}
	f.AssertDone(t)
}

func TestSSHConfigOverride(t *testing.T) {
	t.Setenv("SPINUP_SSH_CONFIG", "/tmp/spinup-test-ssh-config")
	fake := &runexec.Fake{}
	fake.Expect("ssh").Return("", "", 0)
	r := New(Host{Name: "staging", Address: "vm.example.com", Port: 22}, fake, Options{})
	if _, err := r.Run(context.Background(), Cmd{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || len(calls[0].Args) < 2 || calls[0].Args[0] != "-F" || calls[0].Args[1] != "/tmp/spinup-test-ssh-config" {
		t.Fatalf("ssh args = %v, want -F first", calls)
	}
}
