//go:build unix

package consoleinstall

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestConsoleInstallStableMountsAndConcurrentSampling(t *testing.T) {
	f := newCollector(t)
	if err := os.Mkdir(filepath.Join(f.dir, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	f.write("console/data/facts.json", "previous complete snapshot\n", 0644)
	before, err := os.Stat(filepath.Join(f.dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the production shell scripts with a real filesystem and advisory lock.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	runner := runexec.OSRunner{}
	prepared, err := runner.Run(ctx, runexec.Cmd{Name: "sh", Args: []string{"-c", prepareScript, "fixture", f.dir}})
	if err != nil {
		t.Fatal(err)
	}
	stage := strings.TrimSpace(string(prepared.Stdout))
	t.Cleanup(func() { _ = os.RemoveAll(stage) })
	for _, name := range []string{"compose.console.yml", ".env", "probe.conf", "collect.sh"} {
		if err := os.WriteFile(filepath.Join(stage, name), []byte("new "+name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	installerBin := filepath.Join(f.root, "installer-bin")
	if err := os.Mkdir(installerBin, 0700); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(f.root, "install-lock-attempt")
	// fcntl.flock retains the lock through the shell's inherited descriptor.
	helper := "#!/bin/sh\nprintf ready > \"$INSTALL_LOCK_ATTEMPT\"\nexec python3 -I -c 'import fcntl,sys; fcntl.flock(int(sys.argv[1]), fcntl.LOCK_EX)' \"$2\"\n"
	if err := os.WriteFile(filepath.Join(installerBin, "flock"), []byte(helper), 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(f.dir, "sample.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, runexec.Cmd{Name: "sh", Args: []string{"-c", publishScript, "fixture", f.dir, stage}, Env: []string{"PATH=" + installerBin + ":" + os.Getenv("PATH"), "INSTALL_LOCK_ATTEMPT=" + ready}})
		done <- err
	}()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("publish finished before acquiring lock: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	config, err := os.ReadFile(filepath.Join(f.dir, "probe.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(config), "new") {
		t.Fatal("configuration changed while a sampler held its lock")
	}
	if out, err := f.run(); err != nil {
		t.Fatalf("concurrent sampler: %v: %s", err, out)
	}
	for _, call := range f.calls() {
		if call.Args[0] == "docker" {
			t.Fatal("concurrent sampler did not reuse prior snapshot")
		}
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(f.dir, "data"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("data mount inode changed: %v", err)
	}
	snapshot, err := os.ReadFile(filepath.Join(f.dir, "data/facts.json"))
	if err != nil || string(snapshot) != "previous complete snapshot\n" {
		t.Fatalf("snapshot changed: %s, %v", snapshot, err)
	}
	config, err = os.ReadFile(filepath.Join(f.dir, "probe.conf"))
	if err != nil || string(config) != "new probe.conf\n" {
		t.Fatalf("configuration not published: %s, %v", config, err)
	}
	info, err := os.Stat(filepath.Join(f.dir, "probe.conf"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("configuration mode: %v", err)
	}
}

type sampleRunner struct {
	fixture *collectorFixture
	path    string
}

func (r sampleRunner) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	args := append([]string(nil), c.Argv[1:]...)
	args[len(args)-1] = r.fixture.dir
	return (runexec.OSRunner{}).Run(ctx, runexec.Cmd{Name: c.Argv[0], Args: args, Env: append(r.fixture.env, "PATH="+r.path+":"+os.Getenv("PATH")), Timeout: c.Timeout})
}
func (sampleRunner) Upload(context.Context, []byte, string, fs.FileMode) error {
	return fmt.Errorf("unexpected upload")
}
func (sampleRunner) Close(context.Context) error { return nil }

func TestConsoleRefreshForcesCompletedSample(t *testing.T) {
	f := newCollector(t)
	requireCollector(t, f)
	before, err := os.Stat(filepath.Join(f.dir, "data/facts.json"))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(f.root, "force-bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	helper := `#!/bin/sh
shift
exec python3 -I -c 'import fcntl,sys,subprocess
with open(sys.argv[1], "a") as lock:
    fcntl.flock(lock, fcntl.LOCK_EX)
    sys.exit(subprocess.call(sys.argv[2:]))' "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "flock"), []byte(helper), 0700); err != nil {
		t.Fatal(err)
	}
	// Publish post and the new containers before forcing a sample. The same collector
	// invocation must read both sources after that completed journal entry.
	newContainers := `#!/bin/sh
if [ "$1" = inspect ]; then
 printf '%s\n' '{"project":"sample-blue","service":"api","name":"sample-blue-api-1","state":"running","health":"healthy"}' '{"project":"sample-console","service":"console","name":"sample-console-console-1","state":"running","health":"healthy"}'
else
 exec "$COLLECTOR_TEST_BINARY" -test.run '^TestCollectorUtility$' -- docker "$@"
fi
`
	if err := os.WriteFile(filepath.Join(f.bin, "docker"), []byte(newContainers), 0700); err != nil {
		t.Fatal(err)
	}
	f.write("deploy.state", `{"release":"20261122T180000Z-0123456789ab","step":"post","status":"done","at":"2026-11-22T18:00:00Z","detail":"ok"}`+"\n", 0600)
	if err := ForceRefresh(t.Context(), sampleRunner{fixture: f, path: bin}, "sample"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(f.dir, "data/facts.json"))
	if err != nil || os.SameFile(before, after) {
		t.Fatalf("forced sample reused the previous snapshot: %v", err)
	}
	captured, err := os.ReadFile(filepath.Join(f.captured, "deploy.state"))
	if err != nil || !strings.Contains(string(captured), `"status":"done"`) {
		t.Fatalf("sampler missed completed history: %s, %v", captured, err)
	}
	containers, err := os.ReadFile(filepath.Join(f.captured, "containers.ndjson"))
	if err != nil || !strings.Contains(string(containers), `"name":"sample-console-console-1"`) || !strings.Contains(string(containers), `"name":"sample-blue-api-1"`) {
		t.Fatalf("same sample missed console containers: %s, %v", containers, err)
	}
}
