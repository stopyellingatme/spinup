package deploy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDeployFilesBackupBackend(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"pgdump", "pgbackrest", ""} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, true, "")
			name := filepath.Join(x.root, "spinup.toml")
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("\n[backups]\nbackend = \""+backend+"\"\n")...)
			if err := os.WriteFile(name, data, 0600); err != nil {
				t.Fatal(err)
			}
			writeTree(t, x.root, map[string]string{"deploy/compose.backups.yml": "services: {}\n"})
			files, err := deployFiles(x.root)
			if err != nil {
				t.Fatal(err)
			}
			got := slices.Contains(edgeComposeFiles(files), "compose.backups.yml")
			if got != (backend != "pgdump") {
				t.Fatalf("backend %q: archive override enabled = %v", backend, got)
			}
		})
	}
}
