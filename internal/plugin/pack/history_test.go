package pack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/plugin"
)

func TestHistoricalCacheRequiresProjectHash(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing-history", "wrong-hash", "wrong-source", "wrong-receipt", "missing-cache"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			m := manager(t)
			root := fixture(t)
			const name = "example"
			source := "path:" + root
			if err := m.Add(t.Context(), source, ""); err != nil {
				t.Fatal(err)
			}
			old, err := m.resolve(t.Context(), plugin.Options{Root: m.Root})
			if err != nil {
				t.Fatal(err)
			}
			dir, err := m.cacheDir(name, "1.0.0", source, old[0].SHA256)
			if err != nil {
				t.Fatal(err)
			}
			replaceLine(t, root+"/plugin.toml", "version", "version = '2.0.0'")
			if err := m.Update(t.Context(), name, "2.0.0"); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing-history":
				if err := os.Remove(filepath.Join(m.Root, historyFile)); err != nil {
					t.Fatal(err)
				}
			case "wrong-hash", "wrong-source":
				entries, err := readHistory(m.Root)
				if err != nil {
					t.Fatal(err)
				}
				for i := range entries {
					if entries[i].Version == "1.0.0" {
						if kind == "wrong-hash" {
							entries[i].SHA256 = strings.Repeat("0", 64)
						} else {
							entries[i].Source = "path:elsewhere"
						}
					}
				}
				data, err := json.Marshal(entries)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(m.Root, historyFile), data, 0644); err != nil {
					t.Fatal(err)
				}
			case "wrong-receipt":
				data, err := json.Marshal(receipt{Source: source, SHA256: strings.Repeat("0", 64)})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "receipt.json"), data, 0644); err != nil {
					t.Fatal(err)
				}
			case "missing-cache":
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := m.resolve(t.Context(), plugin.Options{Root: m.Root, Versions: map[string]string{name: "1.0.0"}}); err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("unverified OLD=%v error=%v", got, err)
			}
		})
	}
}

func TestSameVersionApprovalRetainsCache(t *testing.T) {
	t.Parallel()
	m := manager(t)
	root := fixture(t)
	source := "path:" + root
	if err := m.Add(t.Context(), source, ""); err != nil {
		t.Fatal(err)
	}
	old, err := m.resolve(t.Context(), plugin.Options{Root: m.Root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "files/example.txt.tmpl"), []byte("new approval\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(t.Context(), "example", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.cached("example", "1.0.0", source, old[0].SHA256); err != nil {
		t.Fatalf("old tree was overwritten: %v", err)
	}
	// A version-only OLD request cannot choose between multiple approved hashes.
	if _, err := m.resolve(t.Context(), plugin.Options{Root: m.Root, Versions: map[string]string{"example": "1.0.0"}}); err == nil || !strings.Contains(err.Error(), "multiple hashes") {
		t.Fatalf("ambiguous OLD = %v", err)
	}
	if err := m.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
}
