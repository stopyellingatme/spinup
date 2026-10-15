package pgupgrade

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/maintain/pg"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// operationRunner executes only reservation and archive commands on a temporary
// filesystem. Docker and SSH remain fake throughout the complete upgrade flow.
type operationRunner struct {
	*remote.Fake
	dir string
}

func (r operationRunner) Run(ctx context.Context, cmd remote.Cmd) (runexec.Result, error) {
	result, err := r.Fake.Run(ctx, cmd)
	if err != nil {
		return result, err
	}
	reservation := slices.Equal(cmd.Argv, []string{"mkdir", "-m", "0700", op})
	directoryScript := len(cmd.Argv) > 4 && cmd.Argv[0] == "sh" && cmd.Argv[1] == "-ec" && slices.Contains(cmd.Argv, op)
	if reservation || directoryScript {
		args := slices.Clone(cmd.Argv)
		for i := range args {
			args[i] = strings.ReplaceAll(args[i], op, r.dir)
		}
		return (runexec.OSRunner{}).Run(ctx, runexec.Cmd{Name: args[0], Args: args[1:]})
	}
	return result, nil
}

func TestFailedCheckAllowsRetry(t *testing.T) {
	t.Parallel()
	o, f, out := fixture(t)
	r := operationRunner{Fake: f, dir: filepath.Join(t.TempDir(), "pg-upgrade")}
	deps := pg.Deps{Remote: r, HTTPGet: func(context.Context, string) (int, []byte, error) {
		return 200, []byte(`{"version":"abcdef123456"}`), nil
	}}
	lock(f)
	inspect(f, "17.11", "/var/lib/postgresql/data/pgdata")
	beforeCheck(f, "demo-pg-upgrade:17-18")
	f.Expect("docker", "run", "--rm").Fail(errors.New("extension missing"))
	f.Expect("docker", "run", "--rm") // remove target
	f.Expect("docker", "compose", "-p", "demo")
	f.Expect("cat", release+"/release.json").Return(`{"color":"blue"}`, "", 0)
	f.Expect("docker", "compose", "-p", "demo-blue")
	f.Expect("docker", "rm", "demo-pg-recovery-17-11")
	f.Expect("sh", "-ec")
	f.Expect("sh", "-c")
	if err := Run(context.Background(), o, deps); err == nil || !strings.Contains(err.Error(), "extension missing") {
		t.Fatalf("check failure = %v", err)
	}
	f.AssertDone(t)
	if _, err := os.Lstat(r.dir); !os.IsNotExist(err) {
		t.Fatalf("failed check retained the operation directory: %v", err)
	}
	if strings.Contains(out.String(), "Recovery instructions:") {
		t.Fatalf("printed a removed recovery script: %s", out)
	}
	lock(f)
	inspect(f, "17.11", "/var/lib/postgresql/data/pgdata")
	beforeCheck(f, "demo-pg-upgrade:17-18")
	f.Expect("docker", "run", "--rm")
	f.ExpectUpload(op + "/rollback.sh")
	f.Expect("docker", "run", "--rm")
	afterLink(f, false)
	f.Expect("sh", "-c")
	if err := Run(context.Background(), o, deps); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	f.AssertDone(t)
}

func (r operationRunner) Upload(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	if err := r.Fake.Upload(ctx, data, path, mode); err != nil {
		return err
	}
	if strings.HasPrefix(path, op+"/") {
		return os.WriteFile(filepath.Join(r.dir, strings.TrimPrefix(path, op+"/")), data, mode)
	}
	return nil
}

func TestUpgradeCleanupThenSecondUpgrade(t *testing.T) {
	t.Parallel()
	o, f, _ := fixture(t)
	r := operationRunner{Fake: f, dir: filepath.Join(t.TempDir(), "pg-upgrade")}
	deps := pg.Deps{Remote: r, HTTPGet: func(context.Context, string) (int, []byte, error) {
		return 200, []byte(`{"version":"abcdef123456"}`), nil
	}}
	o.To = 17
	lock(f)
	inspect(f, "16.15", "/var/lib/postgresql/data/pgdata")
	beforeCheck(f, "demo-pg-upgrade:16-17")
	f.Expect("docker", "run", "--rm") // check
	f.ExpectUpload(op + "/rollback.sh")
	f.Expect("docker", "run", "--rm") // link
	afterLink(f, false)
	f.Expect("sh", "-c")
	if err := Run(context.Background(), o, deps); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)

	stateBytes, err := os.ReadFile(filepath.Join(r.dir, "link-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	l := manifest.New()
	l.Answers["db_version"] = "17"
	if err := l.Save(filepath.Join(o.Dir, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	write(t, o.Dir, "deploy/compose.edge.yml", "services:\n  postgres:\n    environment:\n      PGDATA: /var/lib/postgresql/data/17/docker\nvolumes:\n  postgres_data:\n    external: true\n    name: demo_postgres_data\n")
	o.Cleanup, o.To = true, 0
	lock(f)
	f.Expect("cat", op+"/link-state.json").Return(string(stateBytes), "", 0)
	inspect(f, "17.11", "/var/lib/postgresql/data/17/docker")
	f.Expect("sh", "-ec") // check archive destination
	f.Expect("docker", "run", "--rm")
	f.Expect("sh", "-ec") // archive completed operation
	f.Expect("sh", "-c")
	if err := Run(context.Background(), o, deps); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)
	if _, err := os.Lstat(r.dir); !os.IsNotExist(err) {
		t.Fatalf("operation directory still reserved: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(r.dir+".16-17", "link-state.json")); err != nil || string(got) != string(stateBytes) {
		t.Fatalf("archive lost upgrade state: %q, %v", got, err)
	}

	o.Cleanup, o.To = false, 18
	lock(f)
	inspect(f, "17.11", "/var/lib/postgresql/data/17/docker")
	beforeCheck(f, "demo-pg-upgrade:17-18")
	f.Expect("docker", "run", "--rm") // check
	f.ExpectUpload(op + "/rollback.sh")
	f.Expect("docker", "run", "--rm") // link
	afterLink(f, false)
	f.Expect("sh", "-c")
	if err := Run(context.Background(), o, deps); err != nil {
		t.Fatalf("second upgrade failed: %v", err)
	}
	f.AssertDone(t)
}
