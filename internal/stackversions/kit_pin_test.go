package stackversions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKitPinHasPackSums checks that the api pack go.sum has both hash lines of the pinned kit
// version, and no hash line of another kit version (ADR 0018). Without them, a generated app
// that builds with -mod=readonly fails with "missing go.sum entry" for the kit.
func TestKitPinHasPackSums(t *testing.T) {
	t.Parallel()

	stack, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	const kit = "github.com/stopyellingatme/spinup/kit"
	version, err := stack.GoModule(kit)
	if err != nil {
		t.Fatalf("GoModule(%q) error = %v", kit, err)
	}
	path := filepath.Join("..", "templates", "packs", "api", "files", "api", "go.sum")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		kit + " " + version + " ":         false,
		kit + " " + version + "/go.mod ": false,
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, kit+" ") {
			continue
		}
		matched := false
		for prefix := range want {
			if strings.HasPrefix(line, prefix+"h1:") {
				want[prefix] = true
				matched = true
			}
		}
		if !matched {
			t.Errorf("%s has a hash line of another kit version: %q", path, line)
		}
	}
	for prefix, found := range want {
		if !found {
			t.Errorf("%s lacks the hash line %q for the kit pin %s", path, prefix+"h1:...", version)
		}
	}
	if t.Failed() {
		t.Log("Try: run scripts/bump-stack.sh, or copy the kit lines from the go.sum of an app that requires the pin")
	}
}
