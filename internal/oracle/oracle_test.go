package oracle

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "regenerate the R0.5 oracle fixtures")
var rustArchive = flag.String("rust-archive", "", "also validate a Rust archive with the Go reader")

func TestFixtures(t *testing.T) {
	first, second := generate(t), generate(t)
	for name, data := range first {
		if !bytes.Equal(data, second[name]) {
			t.Fatalf("non-deterministic fixture: %s", name)
		}
		filename := filepath.Join("../../testdata/oracle", name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filename, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(filename)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("fixture %s differs: %v. Run go test ./internal/oracle -update", name, err)
		}
	}
	if *rustArchive != "" {
		checkRustArchive(t, *rustArchive, first["archive-entries.json"])
	}
}
