package backup

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// localVM runs remote commands on this machine and maps /opt/<app> to a local directory.
// The integration test uses it in place of SSH.
type localVM struct {
	app, opt string
}

func (l localVM) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	return runexec.OSRunner{}.Run(ctx, runexec.Cmd{Name: c.Argv[0], Args: c.Argv[1:], Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr, Timeout: c.Timeout, Redact: c.Redact})
}

func (l localVM) Upload(_ context.Context, data []byte, path string, mode fs.FileMode) error {
	local := filepath.Join(l.opt, strings.TrimPrefix(path, "/opt/"+l.app+"/"))
	return os.WriteFile(local, data, mode)
}

func (l localVM) Close(context.Context) error { return nil }

// TestPGBackRestIntegration runs real backups against a running postgres container of the
// Compose project SPINUP_BACKUP_IT_APP (compose.edge.yml plus compose.backups.yml).
// SPINUP_BACKUP_IT_OPT is the local directory that stands for /opt/<app>, and
// SPINUP_BACKUP_IT_CONF is the rendered pgbackrest.conf.
func TestPGBackRestIntegration(t *testing.T) {
	app, opt, conf := os.Getenv("SPINUP_BACKUP_IT_APP"), os.Getenv("SPINUP_BACKUP_IT_OPT"), os.Getenv("SPINUP_BACKUP_IT_CONF")
	if app == "" || opt == "" || conf == "" {
		t.Skip("set SPINUP_BACKUP_IT_APP, SPINUP_BACKUP_IT_OPT, and SPINUP_BACKUP_IT_CONF to run real backups")
	}
	data, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	vm := localVM{app: app, opt: opt}
	var progress strings.Builder
	b := &PGBackRest{App: app, Remote: vm, Config: data, Progress: &progress}
	for _, kind := range []Kind{Full, Diff} {
		result, err := b.Backup(ctx, kind)
		if err != nil {
			t.Fatalf("%s backup: %v\n%s", kind, err, progress.String())
		}
		t.Logf("%s backup: %+v", kind, result)
		if result.Kind != kind || result.Label == "" || result.Size <= 0 {
			t.Fatalf("result = %+v", result)
		}
	}
	out, err := b.run(ctx, "info", "--output=json", "info")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("info JSON: %s", out)
	status, err := b.Info(ctx)
	if err != nil || !status.Exists || len(status.Backups) < 2 || status.Last.Kind != Diff || status.Cipher != "aes-256-cbc" {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	running, err := postgresRunning(ctx, vm, app)
	if err != nil || !running {
		t.Fatalf("postgresRunning = %v, %v", running, err)
	}
	version, err := AppliedVersion(ctx, vm, app)
	if err != nil || version != 0 {
		t.Fatalf("AppliedVersion = %d, %v", version, err)
	}
}
