package remote

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

func testKey(fill byte) string {
	blob := binary.BigEndian.AppendUint32(nil, 11)
	blob = append(blob, "ssh-ed25519"...)
	blob = binary.BigEndian.AppendUint32(blob, 32)
	for i := 0; i < 32; i++ {
		blob = append(blob, fill)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
}

func runArgs(t *testing.T, h Host, o Options) ([]string, error) {
	t.Helper()
	f := &runexec.Fake{}
	f.Expect("ssh").Return("", "", 0)
	r := New(h, f, o)
	if _, err := r.Run(context.Background(), Cmd{Argv: []string{"true"}}); err != nil {
		return nil, err
	}
	return f.Calls()[0].Args, nil
}

func TestPinnedArgsAndFile(t *testing.T) {
	cache := shortCache(t)
	k1, k2 := testKey(1), testKey(2)
	h := Host{Name: "prod", Address: "vm", User: "deploy", HostKeys: []string{k2 + " root@droplet", k1}}
	args, err := runArgs(t, h, Options{CacheDir: cache, AcceptNewHostKey: true})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("deploy@vm:22"))
	file := filepath.Join(cache, "spinup", "ssh", "known_hosts-"+hex.EncodeToString(sum[:])[:16])
	want := []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + filepath.Join(cache, "spinup", "ssh", "%C"), "-o", "ControlPersist=60s",
		"-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "UserKnownHostsFile=" + file, "-o", "GlobalKnownHostsFile=/dev/null", "-o", "StrictHostKeyChecking=yes",
		"-o", "UpdateHostKeys=no", "-o", "CheckHostIP=no", "-o", "HostKeyAlgorithms=ssh-ed25519",
		"--", "deploy@vm", "'true'"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v\nwant %#v", args, want)
	}
	for _, a := range args {
		if strings.Contains(a, "accept-new") {
			t.Fatal("accept-new must not be set")
		}
	}
	wantContent := "* " + k1 + "\n* " + k2 + "\n"
	data, err := os.ReadFile(file)
	if err != nil || string(data) != wantContent {
		t.Fatalf("content = %q, %v", data, err)
	}
	info, _ := os.Stat(file)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	// A second New leaves an identical file alone.
	before, _ := os.Stat(file)
	if _, err := runArgs(t, h, Options{CacheDir: cache}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(file)
	if !os.SameFile(before, after) {
		t.Fatal("identical file was rewritten")
	}
	// A changed file is repaired.
	if err := os.WriteFile(file, []byte("junk"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runArgs(t, h, Options{CacheDir: cache}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(file); string(data) != wantContent {
		t.Fatalf("not repaired: %q", data)
	}
	entries, _ := os.ReadDir(filepath.Join(cache, "spinup", "ssh"))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestPinnedPercentEscaped(t *testing.T) {
	cache := filepath.Join(shortCache(t), "a%b")
	args, err := runArgs(t, Host{Name: "p", Address: "vm", HostKeys: []string{testKey(3)}}, Options{CacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	var found string
	for _, a := range args {
		if strings.HasPrefix(a, "UserKnownHostsFile=") {
			found = a
		}
	}
	if !strings.Contains(found, "a%%b") || strings.Contains(strings.ReplaceAll(found, "%%", ""), "%") {
		t.Fatalf("percent not escaped: %q", found)
	}
}

func TestPinnedInvalidKeys(t *testing.T) {
	good := testKey(4)
	short := base64.StdEncoding.EncodeToString([]byte("nope"))
	for name, key := range map[string]string{
		"empty":     "",
		"algorithm": "ssh-rsa " + strings.Fields(good)[1],
		"no data":   "ssh-ed25519",
		"base64":    "ssh-ed25519 !!!",
		"blob":      "ssh-ed25519 " + short,
	} {
		_, err := runArgs(t, Host{Name: "prod", Address: "vm", HostKeys: []string{key}}, Options{CacheDir: shortCache(t)})
		if err == nil || !strings.Contains(err.Error(), "Try: fix host_keys in spinup.toml") || !strings.Contains(err.Error(), `"prod"`) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestUnpinnedArgsUnchanged(t *testing.T) {
	cache := shortCache(t)
	args, err := runArgs(t, Host{Address: "vm", Port: 22}, Options{CacheDir: cache, AcceptNewHostKey: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + filepath.Join(cache, "spinup", "ssh", "%C"), "-o", "ControlPersist=60s",
		"-p", "22", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", "--", "vm", "'true'"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v", args)
	}
}

func TestPinnedAfterConfigFile(t *testing.T) {
	t.Setenv("SPINUP_SSH_CONFIG", "/tmp/cfg")
	args, err := runArgs(t, Host{Address: "vm", HostKeys: []string{testKey(5)}}, Options{CacheDir: shortCache(t)})
	if err != nil || args[0] != "-F" || args[1] != "/tmp/cfg" {
		t.Fatalf("args = %v, %v", args, err)
	}
}

func TestIsHostKeyError(t *testing.T) {
	for s, want := range map[string]bool{
		"@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@":                    true,
		"Host key verification failed.":                                               true,
		"No ED25519 host key is known for vm and you have requested strict checking.": true,
		"Connection refused": false,
		"":                   false,
	} {
		if got := IsHostKeyError(s); got != want {
			t.Errorf("IsHostKeyError(%q) = %v", s, got)
		}
	}
}

func pinFiles(t *testing.T, cache string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(cache, "spinup", "ssh"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "known_hosts") {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestPinFilePerHostAndPort(t *testing.T) {
	cache := shortCache(t)
	key := testKey(6)
	for _, h := range []Host{
		{Name: "a", Address: "vm1", User: "deploy", HostKeys: []string{key}},
		{Name: "a", Address: "vm1", User: "deploy", Port: 22, HostKeys: []string{key}},
		{Name: "b", Address: "vm1", User: "deploy", Port: 2222, HostKeys: []string{key}},
		{Name: "c", Address: "vm2", User: "deploy", HostKeys: []string{key}},
		{Name: "d", Address: "vm1", User: "other", HostKeys: []string{key}},
	} {
		if _, err := runArgs(t, h, Options{CacheDir: cache}); err != nil {
			t.Fatal(err)
		}
	}
	// Port 0 and port 22 share a file. The other four hosts each have their own.
	if got := pinFiles(t, cache); len(got) != 4 {
		t.Fatalf("pin files = %v, want 4", got)
	}
}

func TestPinFileReplacedWhenKeysChange(t *testing.T) {
	cache := shortCache(t)
	h := Host{Name: "prod", Address: "vm", User: "deploy", HostKeys: []string{testKey(7)}}
	if _, err := runArgs(t, h, Options{CacheDir: cache}); err != nil {
		t.Fatal(err)
	}
	h.HostKeys = []string{testKey(8), testKey(9)}
	if _, err := runArgs(t, h, Options{CacheDir: cache}); err != nil {
		t.Fatal(err)
	}
	files := pinFiles(t, cache)
	if len(files) != 1 {
		t.Fatalf("pin files = %v, want 1", files)
	}
	data, err := os.ReadFile(filepath.Join(cache, "spinup", "ssh", files[0]))
	want := "* " + testKey(8) + "\n* " + testKey(9) + "\n"
	if err != nil || string(data) != want {
		t.Fatalf("content = %q, %v", data, err)
	}
}

func TestPinFileConcurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Rename cannot replace a file that another process has open on Windows; remote commands need WSL2")
	}
	cache := shortCache(t)
	h := Host{Name: "prod", Address: "vm", User: "deploy", HostKeys: []string{testKey(10)}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := &runexec.Fake{}
			f.Expect("ssh").Return("", "", 0)
			r := New(h, f, Options{CacheDir: cache})
			_, err := r.Run(context.Background(), Cmd{Argv: []string{"true"}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := pinFiles(t, cache); len(got) != 1 {
		t.Fatalf("pin files = %v, want 1", got)
	}
}
