package templates_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/doctor/composelint"
	spexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

// backupsFiles lists the files of the backups pack.
var backupsFiles = []string{
	"deploy/compose.backups.yml", "deploy/pgbackrest/pgbackrest.conf", "deploy/backups/run.sh",
	"deploy/systemd/spinup-todo-backup-full.service", "deploy/systemd/spinup-todo-backup-full.timer",
	"deploy/systemd/spinup-todo-backup-diff.service", "deploy/systemd/spinup-todo-backup-diff.timer",
	"deploy/systemd/spinup-todo-backup-verify.service", "deploy/systemd/spinup-todo-backup-verify.timer",
}

func renderBackups(t *testing.T, answers map[string]any) (fs.FS, fs.FS) {
	t.Helper()
	all := testutil.RenderPack(t, []string{"base", "api", "web", "deploy", "backups"}, answers)
	selected := fstest.MapFS{}
	for _, name := range backupsFiles {
		data, err := fs.ReadFile(all, name)
		if err != nil {
			t.Fatal(err)
		}
		info, err := fs.Stat(all, name)
		if err != nil {
			t.Fatal(err)
		}
		selected[name] = &fstest.MapFile{Data: data, Mode: info.Mode()}
	}
	return selected, all
}

func TestGoldenBackups(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "backups")
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "ci,deploy,backups"}
	render := func() (fs.FS, error) {
		selected, _ := renderBackups(t, answers)
		return selected, nil
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "backups", fsys)
	testutil.Deterministic(t, render)

	_, all := renderBackups(t, answers)
	checkBackupsTree(t, all, "/var/lib/postgresql/18/docker")

	t.Run("postgres-16", func(t *testing.T) {
		t.Parallel()
		_, all := renderBackups(t, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "ci,deploy,backups", "db_version": "16"})
		checkBackupsTree(t, all, "/var/lib/postgresql/data/pgdata")
	})
}

// checkBackupsTree checks the contracts that a golden diff does not explain: the Compose
// security lint, the merged Compose configuration, the stanza, and the secret loader.
func checkBackupsTree(t *testing.T, all fs.FS, pgdata string) {
	t.Helper()
	override := readDeployFile(t, all, "deploy/compose.backups.yml")
	findings, err := composelint.Lint("deploy/compose.backups.yml", []byte(override))
	if err != nil {
		t.Fatalf("lint compose.backups.yml: %v", err)
	}
	for _, finding := range findings {
		t.Errorf("%s: %s: %s: %s", finding.File, finding.Service, finding.Rule, finding.Message)
	}
	conf := readDeployFile(t, all, "deploy/pgbackrest/pgbackrest.conf")
	for _, want := range []string{
		"repo1-type=s3\n", "repo1-cipher-type=aes-256-cbc\n", "repo1-path=/todo\n", "repo1-retention-full=4\n",
		"\n[todo]\n", "pg1-path=" + pgdata + "\n", "spool-path=/var/lib/pgbackrest/spool\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("pgbackrest.conf does not contain %q", want)
		}
	}
	// Secrets come from the environment only. The file holds no key or passphrase option.
	for _, forbidden := range []string{"repo1-s3-key", "repo1-cipher-pass", "repo1-s3-bucket", "repo1-s3-endpoint"} {
		if strings.Contains(conf, forbidden+"=") {
			t.Errorf("pgbackrest.conf sets the secret option %s", forbidden)
		}
	}
	for _, want := range []string{
		"archive_mode=on\n",
		"archive_command=pgbackrest --stanza=todo archive-push %p\n",
		"config_file=/etc/postgresql/postgresql.conf\n",
		"PGBACKREST_CONFIG: /var/lib/pgbackrest/pgbackrest.conf\n",
		"- /opt/todo/backups:/var/lib/pgbackrest\n",
		"name: todo-backup\n",
		// compose.backups.yml and the backup package must load the secrets the same way.
		strings.ReplaceAll(backup.SecretLoader, "$", "$$") + `exec docker-entrypoint.sh "$$@"`,
	} {
		if !strings.Contains(override, want) {
			t.Errorf("compose.backups.yml does not contain %q", want)
		}
	}
	if strings.Contains(override, "ports:") {
		t.Error("compose.backups.yml publishes a port")
	}

	mergedEdge(t, writeDeployTree(t, all))
}

// mergedEdge merges compose.edge.yml and compose.backups.yml with `docker compose config`
// and checks the merged postgres service. The command needs the docker CLI, but no daemon.
func mergedEdge(t *testing.T, dir string) {
	t.Helper()
	if _, err := spexec.LookPath("docker"); err != nil {
		t.Logf("skip docker compose config: docker is not on PATH")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := spexec.OSRunner{}.Run(ctx, spexec.Cmd{
		Name: "docker",
		Args: []string{"compose", "-f", "compose.edge.yml", "-f", "compose.backups.yml", "config", "--format", "json"},
		Dir:  dir,
		Env:  []string{"DOCKER_CLI_HINTS=false", "PUBLIC_HOST=todo.example.com"},
	})
	if err != nil {
		t.Fatalf("docker compose config: %v\n%s", err, result.Stderr)
	}
	var merged struct {
		Services map[string]struct {
			Entrypoint []string          `json:"entrypoint"`
			Command    []string          `json:"command"`
			ReadOnly   bool              `json:"read_only"`
			CapDrop    []string          `json:"cap_drop"`
			Networks   map[string]any    `json:"networks"`
			Ports      []any             `json:"ports"`
			Env        map[string]string `json:"environment"`
			Volumes    []struct {
				Source, Target string
				ReadOnly       bool `json:"read_only"`
			} `json:"volumes"`
		} `json:"services"`
		Networks map[string]struct {
			Name     string `json:"name"`
			Internal bool   `json:"internal"`
		} `json:"networks"`
	}
	if err := json.Unmarshal(result.Stdout, &merged); err != nil {
		t.Fatal(err)
	}
	pg := merged.Services["postgres"]
	// docker compose config writes the escaped form ($$) of a literal dollar sign.
	if len(pg.Entrypoint) != 4 || pg.Entrypoint[0] != "sh" || !strings.HasPrefix(strings.ReplaceAll(pg.Entrypoint[2], "$$", "$"), backup.SecretLoader) {
		t.Errorf("postgres entrypoint = %q", pg.Entrypoint)
	}
	if !slices.Contains(pg.Command, "archive_command=pgbackrest --stanza=todo archive-push %p") || pg.Command[0] != "postgres" {
		t.Errorf("postgres command = %q", pg.Command)
	}
	if !pg.ReadOnly || !slices.Contains(pg.CapDrop, "ALL") || len(pg.Ports) != 0 {
		t.Errorf("postgres lost its hardening: read_only=%v cap_drop=%v ports=%v", pg.ReadOnly, pg.CapDrop, pg.Ports)
	}
	if _, ok := pg.Networks["db"]; !ok {
		t.Errorf("postgres left the db network: %v", pg.Networks)
	}
	if _, ok := pg.Networks["backup"]; !ok {
		t.Errorf("postgres is not on the backup network: %v", pg.Networks)
	}
	if !merged.Networks["db"].Internal || merged.Networks["backup"].Internal || merged.Networks["backup"].Name != "todo-backup" {
		t.Errorf("networks = %+v", merged.Networks)
	}
	mounts := map[string]bool{}
	for _, v := range pg.Volumes {
		mounts[v.Source+":"+v.Target] = v.ReadOnly
	}
	for mount, readOnly := range map[string]bool{"/opt/todo/backups:/var/lib/pgbackrest": false, "/opt/todo/secrets:/run/secrets": true, "/opt/todo/postgres:/etc/postgresql": true} {
		if got, ok := mounts[mount]; !ok || got != readOnly {
			t.Errorf("postgres mount %s missing or read_only=%v: %v", mount, got, mounts)
		}
	}
	if pg.Env["PGBACKREST_CONFIG"] != "/var/lib/pgbackrest/pgbackrest.conf" || pg.Env["POSTGRES_PASSWORD_FILE"] == "" {
		t.Errorf("postgres environment = %v", pg.Env)
	}
}
