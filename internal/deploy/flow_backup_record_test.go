package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// backupRecordVM keeps the applied credentials independent of the published release.
type backupRecordVM struct {
	remote.Fake
	current, applied, staged []byte
	remoteHashes             string
	restarts                 int
	mode                     fs.FileMode
	failRestart, failBase    bool
	failHash                 bool
}

// Run simulates a failed deploy while keeping the applied record separate from the current release.
func (v *backupRecordVM) Run(_ context.Context, c remote.Cmd) (runexec.Result, error) {
	if slices.Contains(c.Argv, "spinup-backup-hashes") {
		if v.failHash {
			return runexec.Result{}, errors.New("sha256sum failed")
		}
		return runexec.Result{Stdout: []byte(v.remoteHashes)}, nil
	}
	if slices.Contains(c.Argv, testRoot+"/current/release.json") {
		return runexec.Result{Stdout: v.current}, nil
	}
	if slices.Contains(c.Argv, testRoot+"/postgres/backup-secrets.json") {
		switch c.Argv[0] {
		case "rm":
			v.applied = nil
		case "mv":
			v.applied = slices.Clone(v.staged)
		default:
			return runexec.Result{Stdout: v.applied}, nil
		}
	}
	if slices.Contains(c.Argv, "--force-recreate") {
		v.restarts++
		if v.failRestart {
			v.failRestart = false
			return runexec.Result{}, errors.New("Postgres health check failed")
		}
	} else if slices.Contains(c.Argv, "up") && v.failBase {
		v.failBase = false
		return runexec.Result{}, errors.New("Caddy failed after Postgres restarted")
	}
	if slices.Contains(c.Argv, "migrate") {
		return runexec.Result{}, errors.New("migration failed after edge")
	}
	if slices.Contains(c.Argv, "ps") {
		return runexec.Result{Stdout: []byte("caddy1\n")}, nil
	}
	return runexec.Result{}, nil
}

// Upload stages the applied hashes and records their file mode.
func (v *backupRecordVM) Upload(_ context.Context, data []byte, path string, mode fs.FileMode) error {
	if path == testRoot+"/postgres/backup-secrets.json.next" {
		v.staged, v.mode = slices.Clone(data), mode
	}
	return nil
}

// TestBackupSecretsAfterFailedRelease checks credential recovery after failures before a release becomes current.
func TestBackupSecretsAfterFailedRelease(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"migration", "Postgres health", "base start"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			testBackupSecretsAfterFailure(t, failure)
		})
	}
}

func testBackupSecretsAfterFailure(t *testing.T, failure string) {
	t.Helper()
	x := newFx(t, false, "")
	backupFixture(t, x, "pgbackrest", "posix")
	x.decrypted["PGBACKREST_REPO1_CIPHER_PASS"] = "credential-A"
	x.expectLocalGit()
	x.o.fill()
	l, err := preflight(t.Context(), x.o, x.d, &x.out, false)
	if err != nil {
		t.Fatal(err)
	}
	hashes := SecretHashes(x.decrypted)
	current, _ := json.Marshal(Release{Secrets: hashes})
	applied, _ := json.Marshal(map[string]string{"PGBACKREST_REPO1_CIPHER_PASS": hashes["PGBACKREST_REPO1_CIPHER_PASS"]})
	vm := &backupRecordVM{current: current, applied: applied, failRestart: failure == "Postgres health", failBase: failure == "base start"}
	f := &flow{o: x.o, d: x.d, l: l, vm: vm, out: &x.out, release: testRelease}
	l.secrets = maps.Clone(x.decrypted)
	l.secrets["PGBACKREST_REPO1_CIPHER_PASS"] = "credential-B"
	_, err = f.edge(t.Context())
	if failure == "migration" {
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.migrate(t.Context())
	}
	if err == nil {
		t.Fatal("expected a failed deployment after Postgres restarted")
	}
	l.secrets = maps.Clone(x.decrypted)
	if _, err := f.edge(t.Context()); err != nil {
		t.Fatal(err)
	}
	if vm.restarts != 2 {
		t.Fatalf("Postgres restarted %d times, want B followed by restored A", vm.restarts)
	}
	var got map[string]string
	if err := json.Unmarshal(vm.applied, &got); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, map[string]string{"PGBACKREST_REPO1_CIPHER_PASS": hashes["PGBACKREST_REPO1_CIPHER_PASS"]}) || vm.mode != 0600 {
		t.Fatalf("applied record = %v, mode = %o", got, vm.mode)
	}
	if strings.Contains(string(vm.applied), "credential-") {
		t.Fatal("applied record contains a secret value")
	}
}

// TestBackupSecretsResumeUsesUploadedHashes keeps the applied record accurate when local secrets change after upload.
func TestBackupSecretsResumeUsesUploadedHashes(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	backupFixture(t, x, "pgbackrest", "posix")
	x.decrypted["PGBACKREST_REPO1_CIPHER_PASS"] = "uploaded-value"
	uploaded := SecretHashes(x.decrypted)
	x.decrypted["PGBACKREST_REPO1_CIPHER_PASS"] = "local-value-after-upload"
	x.expectLocalGit()
	x.o.fill()
	l, err := preflight(t.Context(), x.o, x.d, &x.out, false)
	if err != nil {
		t.Fatal(err)
	}
	f := &flow{o: x.o, d: x.d, l: l, vm: x.vm, out: &x.out, release: testRelease}
	x.vm.Expect("sh", "-c").Return(journalUpTo(t, "upload"), "", 0)
	rel, _ := json.Marshal(Release{Release: testRelease, Secrets: uploaded, Images: []Image{{Service: "api", Ref: "todo-api:" + testRelease}}})
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(rel), "", 0)
	if next, err := f.resume(t.Context()); err != nil || Steps[next] != "edge" {
		t.Fatalf("resume: next = %d, error = %v", next, err)
	}
	vm := &backupRecordVM{remoteHashes: uploaded["PGBACKREST_REPO1_CIPHER_PASS"] + "  PGBACKREST_REPO1_CIPHER_PASS\n"}
	f.vm = vm
	if _, err := f.edge(t.Context()); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(vm.applied, &got); err != nil {
		t.Fatal(err)
	}
	if got["PGBACKREST_REPO1_CIPHER_PASS"] != uploaded["PGBACKREST_REPO1_CIPHER_PASS"] {
		t.Fatalf("resume recorded local secrets instead of uploaded secrets: %v", got)
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
}

// TestBackupSecretsResumeRejectsChangedVMSecrets refuses hashes that Postgres will not load.
func TestBackupSecretsResumeRejectsChangedVMSecrets(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"changed", "added", "removed", "hash failure", "invalid checksum"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			values := map[string]string{"PGBACKREST_REPO1_PATH": "uploaded-path"}
			wanted := backupSecretHashes(values)
			switch change {
			case "changed":
				values["PGBACKREST_REPO1_PATH"] = "pushed-path"
			case "added":
				values["PGBACKREST_LOG_LEVEL_CONSOLE"] = "detail"
			case "removed":
				delete(values, "PGBACKREST_REPO1_PATH")
			}
			vm := &backupRecordVM{applied: []byte("{}")}
			for name, hash := range backupSecretHashes(values) {
				vm.remoteHashes += fmt.Sprintf("%s  %s\n", hash, name)
			}
			vm.failHash = change == "hash failure"
			if change == "invalid checksum" {
				vm.remoteHashes = "invalid checksum\n"
			}
			x := newFx(t, false, "")
			x.o.fill()
			f := &flow{o: x.o, l: &local{app: "todo", backups: true}, vm: vm, out: &x.out, release: testRelease, backupHashes: wanted}
			_, err := f.edge(t.Context())
			if err == nil || !strings.Contains(err.Error(), "PGBACKREST_*") || !strings.Contains(err.Error(), "Try: run a new spinup deploy without --resume") {
				t.Fatalf("resume error = %v", err)
			}
			if vm.restarts != 0 || string(vm.applied) != "{}" || len(vm.staged) != 0 {
				t.Fatal("resume changed Postgres or its record after the VM secrets changed")
			}
			for _, value := range []string{"uploaded-path", "pushed-path", "detail"} {
				if strings.Contains(err.Error()+x.out.String(), value) {
					t.Fatal("resume exposed a secret value")
				}
			}
		})
	}
}

// TestRemoteBackupSecretHashes checks the actual shell command against the manifest hash format.
func TestRemoteBackupSecretHashes(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "VM with spaces")
			if err := os.MkdirAll(filepath.Join(root, "secrets"), 0700); err != nil {
				t.Fatal(err)
			}
			values := map[string]string{"API_TOKEN": "unrelated"}
			if !empty {
				values["PGBACKREST_REPO1_PATH"] = "line one\nline two\n"
				values["PGBACKREST_REPO1_CIPHER_PASS"] = ""
			}
			for name, value := range values {
				if err := os.WriteFile(filepath.Join(root, "secrets", name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f := &flow{l: &local{app: "app"}, vm: localVM{root: root}}
			got, err := f.remoteBackupSecretHashes(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, backupSecretHashes(values)) {
				t.Fatalf("remote hashes = %v, want %v", got, backupSecretHashes(values))
			}
		})
	}
}

// TestBackupSecretsEmptyAfterFailedRestart recovers a removed override without any remaining backup secrets.
func TestBackupSecretsEmptyAfterFailedRestart(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	x.o.fill()
	vm := &backupRecordVM{applied: []byte("{}"), failRestart: true}
	l := &local{app: "todo", backups: true, secrets: map[string]string{"PGBACKREST_REPO1_PATH": "/new-backups"}}
	f := &flow{o: x.o, l: l, vm: vm, out: &x.out, release: testRelease}
	if _, err := f.edge(t.Context()); err == nil {
		t.Fatal("expected a failed health wait after Postgres started with the path override")
	}
	if vm.applied != nil {
		t.Fatal("failed restart left an applied record")
	}
	delete(l.secrets, "PGBACKREST_REPO1_PATH")
	if _, err := f.edge(t.Context()); err != nil {
		t.Fatal(err)
	}
	if vm.restarts != 2 || string(vm.applied) != "{}\n" || vm.mode != 0600 {
		t.Fatalf("restarts = %d, record = %q, mode = %o, want a restart and an empty record", vm.restarts, vm.applied, vm.mode)
	}
	if _, err := f.edge(t.Context()); err != nil {
		t.Fatal(err)
	}
	if vm.restarts != 2 {
		t.Fatal("a recorded empty set caused another restart")
	}
}
