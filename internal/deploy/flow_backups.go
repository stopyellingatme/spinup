package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func usesPGBackRest(config *project.Config) bool {
	return slices.Contains(config.Modules, "backups") && (config.Backups.Backend == "" || config.Backups.Backend == "pgbackrest")
}

// backupSecrets lists the required storage secrets for the effective repository type.
// Environment secrets override options in pgbackrest.conf.
func backupSecrets(root string, config *project.Config, values map[string]string) ([]string, error) {
	if !usesPGBackRest(config) {
		return nil, nil
	}
	name := "deploy/pgbackrest/pgbackrest.conf"
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return nil, preflightError("cannot read "+name+": "+err.Error(), "restore "+name+" with spinup restore")
	}
	global := map[string]string{"repo1-type": "posix", "repo1-s3-key-type": "shared", "repo1-cipher-type": "none"}
	stanza, section := map[string]string{}, ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch section {
		case "global":
			global[key] = value
		case config.App.Name:
			stanza[key] = value
		}
	}
	maps.Copy(global, stanza)
	option := func(name string) string {
		if value := values["PGBACKREST_"+strings.ToUpper(strings.ReplaceAll(name, "-", "_"))]; value != "" {
			return value
		}
		return global[name]
	}
	var need []string
	if option("repo1-cipher-type") != "none" {
		need = append(need, "PGBACKREST_REPO1_CIPHER_PASS")
	}
	if option("repo1-type") == "s3" {
		need = append(need,
			"PGBACKREST_REPO1_S3_ENDPOINT",
			"PGBACKREST_REPO1_S3_BUCKET",
			"PGBACKREST_REPO1_S3_REGION",
		)
		if option("repo1-s3-key-type") == "shared" {
			need = append(need, "PGBACKREST_REPO1_S3_KEY", "PGBACKREST_REPO1_S3_KEY_SECRET")
		}
	}
	return need, nil
}

// backupSecretsChanged includes additions and removals as well as changed values.
func backupSecretsChanged(before map[string]string, values map[string]string) bool {
	return !maps.Equal(before, backupSecretHashes(values))
}

func backupSecretHashes(values map[string]string) map[string]string {
	hashes := SecretHashes(values)
	for name := range hashes {
		if !strings.HasPrefix(name, "PGBACKREST_") {
			delete(hashes, name)
		}
	}
	return hashes
}

// readBackupSecretHashes reads the credentials applied to Postgres, independent of a release.
// A nil map means no record. An empty, non-nil map records that no storage secrets were applied.
func (f *flow) readBackupSecretHashes(ctx context.Context) (map[string]string, error) {
	path := f.root() + "/postgres/backup-secrets.json"
	result, err := f.vm.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", readRemoteFileScript, "spinup-read", path}})
	if err != nil {
		return nil, fmt.Errorf("read applied backup secrets: %w\nTry: check the SSH connection and %s", err, path)
	}
	if len(result.Stdout) == 0 {
		return nil, nil
	}
	var hashes map[string]string
	if err := json.Unmarshal(result.Stdout, &hashes); err != nil {
		return nil, fmt.Errorf("decode applied backup secrets: %w\nTry: remove %s and deploy again to restart Postgres", err, path)
	}
	return hashes, nil
}

// remoteBackupSecretHashes uses SHA-256 of the file bytes, the same form as SecretHashes.
// secrets.Push writes the value bytes without a newline. The command prints only hashes and names.
func (f *flow) remoteBackupSecretHashes(ctx context.Context) (map[string]string, error) {
	result, err := f.vm.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", backupSecretHashesScript, "spinup-backup-hashes", f.root() + "/secrets"}})
	if err != nil {
		return nil, fmt.Errorf("cannot resume: hash PGBACKREST_* secret files on the VM: %w\nTry: run a new spinup deploy without --resume", err)
	}
	hashes := make(map[string]string)
	for line := range strings.SplitSeq(strings.TrimSuffix(string(result.Stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := backupSecretHashLine.FindStringSubmatch(line)
		if parts == nil {
			return nil, fmt.Errorf("cannot resume: unexpected checksum output for PGBACKREST_* secret files\nTry: run a new spinup deploy without --resume")
		}
		hashes[parts[2]] = parts[1]
	}
	return hashes, nil
}

var backupSecretHashLine = regexp.MustCompile(`^([0-9a-f]{64})  (PGBACKREST_[A-Z0-9_]*)$`)

// backupSecretHashesScript expands only backup secret names and passes each file as a separate argument.
const backupSecretHashesScript = `set -eu
cd -- "$1"
set -- PGBACKREST_*
if [ "$1" = 'PGBACKREST_*' ] && [ ! -e "$1" ]; then exit 0; fi
sha256sum -- "$@"`

func (f *flow) writeBackupSecretHashes(ctx context.Context, hashes map[string]string) error {
	path := f.root() + "/postgres/backup-secrets.json"
	data, err := json.Marshal(hashes)
	if err != nil {
		return err
	}
	if err := f.vm.Upload(ctx, append(data, '\n'), path+".next", 0600); err != nil {
		return fmt.Errorf("write applied backup secrets: %w\nTry: check the SSH connection and deploy again", err)
	}
	if _, err := f.cmd(ctx, false, "mv", "-f", "--", path+".next", path); err != nil {
		return fmt.Errorf("install applied backup secrets: %w\nTry: check the SSH connection and deploy again", err)
	}
	return nil
}
