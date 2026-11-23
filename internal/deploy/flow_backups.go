package deploy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/project"
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
	repoType, stanzaType, section := "posix", "", ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "repo1-type" {
			continue
		}
		switch section {
		case "global":
			repoType = strings.TrimSpace(value)
		case config.App.Name:
			stanzaType = strings.TrimSpace(value)
		}
	}
	if stanzaType != "" {
		repoType = stanzaType
	}
	if value := values["PGBACKREST_REPO1_TYPE"]; value != "" {
		repoType = value
	}
	need := []string{"PGBACKREST_REPO1_CIPHER_PASS"}
	if repoType == "s3" {
		need = append(need,
			"PGBACKREST_REPO1_S3_ENDPOINT",
			"PGBACKREST_REPO1_S3_BUCKET",
			"PGBACKREST_REPO1_S3_REGION",
			"PGBACKREST_REPO1_S3_KEY",
			"PGBACKREST_REPO1_S3_KEY_SECRET",
		)
	}
	return need, nil
}

// backupSecretsChanged includes additions and removals as well as changed values.
func backupSecretsChanged(cur *Release, values map[string]string) bool {
	var before map[string]string
	if cur != nil {
		before = cur.Secrets
	}
	after := SecretHashes(values)
	for name, hash := range after {
		if strings.HasPrefix(name, "PGBACKREST_") && before[name] != hash {
			return true
		}
	}
	for name := range before {
		if strings.HasPrefix(name, "PGBACKREST_") {
			if _, ok := after[name]; !ok {
				return true
			}
		}
	}
	return false
}
