package templates_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/migrate"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// TestPacksDigest requires an explicit migration decision for every template change.
func TestPacksDigest(t *testing.T) {
	type record struct{ digest, migration string }
	records := map[string]record{}
	data, err := os.ReadFile("packs.digest")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for lineNo, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("packs.digest:%d: want <pack> <digest> <migration-or-none>", lineNo+1)
		}
		if _, exists := records[fields[0]]; exists {
			t.Fatalf("duplicate pack %q", fields[0])
		}
		decoded, err := hex.DecodeString(fields[1])
		if err != nil || len(decoded) != sha256.Size {
			t.Fatalf("invalid digest for %s", fields[0])
		}
		records[fields[0]] = record{fields[1], fields[2]}
	}
	registered := map[string]bool{"none": true}
	for _, m := range migrate.All() {
		registered[m.ID] = true
	}
	packs := templates.Embedded()
	names, err := templates.List(packs)
	if err != nil {
		t.Fatal(err)
	}
	var updated strings.Builder
	for _, name := range names {
		hash := sha256.New()
		// WalkDir visits files in lexical order. Length framing also hashes file boundaries.
		err := fs.WalkDir(packs, name, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			content, err := fs.ReadFile(packs, path)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(hash, "%s\x00%d\x00", path, len(content))
			_, _ = hash.Write(content)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		digest := hex.EncodeToString(hash.Sum(nil))
		previous, exists := records[name]
		decision := previous.migration
		if !exists || previous.digest != digest {
			decision = "REVIEW"
			if os.Getenv("SPINUP_GOLDEN_UPDATE") != "1" {
				t.Errorf("pack %s changed: run SPINUP_GOLDEN_UPDATE=1 go test -run PacksDigest ./internal/templates/, then review packs.digest", name)
			}
		}
		_, _ = fmt.Fprintf(&updated, "%s %s %s\n", name, digest, decision)
		if !registered[decision] {
			t.Errorf("pack %s: %q needs a registered migration ID or an explicit none decision", name, decision)
		}
	}
	var stale []string
	for name := range records {
		if !slices.Contains(names, name) {
			stale = append(stale, name)
		}
	}
	slices.Sort(stale)
	if len(stale) > 0 {
		t.Errorf("packs.digest lists removed packs: %v", stale)
	}
	if os.Getenv("SPINUP_GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile("packs.digest", []byte(updated.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
