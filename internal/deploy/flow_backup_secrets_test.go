package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

var backupStorageNames = []string{
	"PGBACKREST_REPO1_CIPHER_PASS",
	"PGBACKREST_REPO1_S3_ENDPOINT",
	"PGBACKREST_REPO1_S3_BUCKET",
	"PGBACKREST_REPO1_S3_REGION",
	"PGBACKREST_REPO1_S3_KEY",
	"PGBACKREST_REPO1_S3_KEY_SECRET",
}

func backupFixture(t *testing.T, x *fx, backend, repoType string) {
	t.Helper()
	p := filepath.Join(x.root, "spinup.toml")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	data = append([]byte("modules = ['backups']\n"), data...)
	data = append(data, []byte("\n[backups]\nbackend = '"+backend+"'\n")...)
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	writeTree(t, x.root, map[string]string{
		"deploy/compose.backups.yml":        "services: {}\n",
		"deploy/pgbackrest/pgbackrest.conf": "[global]\nrepo1-type=" + repoType + "\n",
	})
	x.decrypted = maps.Clone(testSecrets)
}

func TestBackupSecretsPreflight(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, backend, repoType, override string
		skip                              bool
		missing                           []string
	}{
		{name: "all absent", repoType: "s3", missing: backupStorageNames},
		{name: "skip backup still refuses", repoType: "s3", skip: true, missing: backupStorageNames},
		{name: "empty cipher", repoType: "s3", missing: []string{backupStorageNames[0]}},
		{name: "non S3", repoType: "posix", missing: []string{backupStorageNames[0]}},
		{name: "secret overrides type", repoType: "s3", override: "posix", missing: []string{backupStorageNames[0]}},
		{name: "secret selects S3", repoType: "posix", override: "s3", missing: backupStorageNames},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			backupFixture(t, x, tc.backend, tc.repoType)
			x.o.SkipBackup = tc.skip
			for _, name := range backupStorageNames {
				if !slices.Contains(tc.missing, name) {
					x.decrypted[name] = "present-value"
				}
			}
			if tc.name == "empty cipher" {
				x.decrypted[backupStorageNames[0]] = ""
			}
			if tc.override != "" {
				x.decrypted["PGBACKREST_REPO1_TYPE"] = tc.override
			}
			x.expectLocalGit()
			err := x.run()
			var ue *ui.Error
			if !errors.As(err, &ue) || !strings.Contains(ue.What, "preflight") || ue.Try == "" {
				t.Fatalf("error = %v", err)
			}
			for _, name := range tc.missing {
				if !strings.Contains(ue.Why, name) {
					t.Errorf("missing %s in %v", name, err)
				}
			}
			if x.dialed != 0 || len(x.built) != 0 {
				t.Fatal("preflight built images or changed the VM")
			}
			x.exec.AssertDone(t)
		})
	}
}

func TestBackupSecretsPreflightOtherBackends(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ backend, repoType, override string }{
		{backend: "pgdump", repoType: "s3"},
		{backend: "walg", repoType: "s3"},
		{backend: "pgbackrest", repoType: "posix"},
		{backend: "pgbackrest", repoType: "s3", override: "posix"},
	} {
		t.Run(tc.backend+"/"+tc.repoType+"/"+tc.override, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			backupFixture(t, x, tc.backend, tc.repoType)
			if tc.backend == "pgbackrest" {
				x.decrypted["PGBACKREST_REPO1_CIPHER_PASS"] = "cipher-value"
			}
			if tc.override != "" {
				x.decrypted["PGBACKREST_REPO1_TYPE"] = tc.override
			}
			x.expectLocalGit()
			x.o.fill()
			if _, err := preflight(t.Context(), x.o, x.d, &x.out, false); err != nil {
				t.Fatal(err)
			}
			x.exec.AssertDone(t)
		})
	}
}

func TestBackupHooksSkipBeforeSecretUpload(t *testing.T) {
	t.Parallel()
	for _, first := range []bool{true, false} {
		t.Run(map[bool]string{true: "no recorded release with running postgres", false: "changed storage secrets"}[first], func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			backupFixture(t, x, "pgbackrest", "s3")
			for _, name := range backupStorageNames {
				x.decrypted[name] = "new-storage-value"
			}
			x.expectLocalGit()
			x.o.fill()
			l, err := preflight(t.Context(), x.o, x.d, &x.out, false)
			if err != nil {
				t.Fatal(err)
			}
			f := &flow{o: x.o, d: x.d, l: l, vm: x.vm, out: &x.out, errOut: &x.errOut, release: testRelease}
			current := ""
			if !first {
				data, _ := json.Marshal(Release{Release: "20261014T080000Z-ffffffffffff", Secrets: SecretHashes(testSecrets)})
				current = string(data)
			}
			x.expectReadCurrent(current)
			// The backup must not query or invoke the database before new secrets reach the VM.
			if _, err := f.hooks(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(x.hookCtx) != 0 {
				t.Fatal("backup hook ran before storage secrets were uploaded")
			}
			want := "no release is recorded"
			if !first {
				want = "storage secrets changed"
			}
			if !strings.Contains(x.out.String(), want) {
				t.Fatalf("output = %s", x.out.String())
			}
			x.vm.AssertDone(t)
		})
	}
}

func TestBackupSecretsRestartPostgres(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"first deploy", "added", "changed", "removed", "unchanged", "unrelated", "unhealthy restart"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			backupFixture(t, x, "pgbackrest", "s3")
			for _, name := range backupStorageNames {
				x.decrypted[name] = "storage-value"
			}
			before := maps.Clone(x.decrypted)
			switch change {
			case "added":
				x.decrypted["PGBACKREST_REPO1_S3_URI_STYLE"] = "path"
			case "changed", "unhealthy restart":
				x.decrypted[backupStorageNames[0]] = "new-cipher-value"
			case "removed":
				before["PGBACKREST_REPO1_S3_URI_STYLE"] = "path"
			case "unrelated":
				x.decrypted["API_TOKEN"] = "new-api-value"
			}
			x.expectLocalGit()
			x.o.fill()
			l, err := preflight(t.Context(), x.o, x.d, &x.out, false)
			if err != nil {
				t.Fatal(err)
			}
			f := &flow{o: x.o, d: x.d, l: l, vm: x.vm, out: &x.out, errOut: &x.errOut, release: testRelease}
			// Exercise the real upload step before edge. The fake enforces the complete order.
			x.expectUpstream("")
			x.ok("mkdir", "-p", "--")
			files, err := f.releaseFiles()
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				x.vm.ExpectUpload(testRelDir + "/" + file.Path)
			}
			x.ok("sh", "-c")
			x.vm.Expect("find", testRoot+"/secrets").Return("", "", 0)
			for _, name := range secretNames(x.decrypted) {
				x.vm.ExpectUpload(testRoot + "/secrets/" + name)
			}
			x.vm.ExpectUpload(testRelDir + "/release.json")
			if _, err := f.upload(t.Context()); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(Release{Secrets: SecretHashes(before)})
			if change == "first deploy" {
				data = nil
			}
			x.expectReadCurrent(string(data))
			x.ok("sh", "-c", edgeSyncScript)
			x.expectUpstream("")
			x.vm.ExpectUpload(testRoot + "/caddy/upstream.caddy")
			x.vm.Expect(psArgv("todo", "caddy")...).Return("caddy1\n", "", 0)
			x.ok("docker", "exec", "caddy1", "caddy", "validate")
			x.ok("sh", "-c", edgeInstallScript)
			composeFiles := []string{testRelDir + "/compose.edge.yml", testRelDir + "/compose.backups.yml"}
			restart := change != "unchanged" && change != "unrelated"
			if restart {
				argv := ComposeArgv("todo", testRelDir, composeFiles, "up", "-d", "--no-deps", "--force-recreate", "--wait", "--wait-timeout", "4", "postgres")
				if change == "unhealthy restart" {
					x.vm.Expect(argv...).Return("", "postgres is unhealthy", 1)
				} else {
					x.ok(argv...)
				}
			}
			if change != "unhealthy restart" {
				x.ok(ComposeArgv("todo", testRelDir, composeFiles, "up", "-d", "--wait", "--wait-timeout", "4")...)
			}
			_, err = f.edge(context.Background())
			if change == "unhealthy restart" {
				if err == nil || !strings.Contains(err.Error(), "restart Postgres") {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(x.out.String(), "Restarting Postgres because PGBACKREST_* secrets changed"); got != map[bool]int{true: 1, false: 0}[restart] {
				t.Fatalf("output = %s", x.out.String())
			}
			x.vm.AssertDone(t)
		})
	}
}

func TestBackupHooksKeepsBackupWithUnchangedSecrets(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	backupFixture(t, x, "pgbackrest", "s3")
	for _, name := range backupStorageNames {
		x.decrypted[name] = "storage-value"
	}
	x.expectLocalGit()
	x.o.fill()
	l, err := preflight(t.Context(), x.o, x.d, &x.out, false)
	if err != nil {
		t.Fatal(err)
	}
	f := &flow{o: x.o, d: x.d, l: l, vm: x.vm, out: &x.out, errOut: &x.errOut, release: testRelease}
	data, _ := json.Marshal(Release{Secrets: SecretHashes(x.decrypted)})
	x.expectReadCurrent(string(data))
	expectAppliedMigrations(x.vm, "1\n2\n")
	if _, err := f.hooks(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(x.hookCtx) != 1 || strings.Contains(x.out.String(), "skipped") {
		t.Fatalf("backup hook calls = %d, output = %s", len(x.hookCtx), x.out.String())
	}
	x.vm.AssertDone(t)
}
