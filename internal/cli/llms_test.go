package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/docsite"
	"github.com/stopyellingatme/spinup/internal/render"
)

func TestLLMSIsCurrent(t *testing.T) {
	root := repoRoot(t)
	docs := filepath.Join(root, "docs")
	want, err := docsite.LLMS(os.DirFS(docs))
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(docs, "llms.txt")
	if os.Getenv("SPINUP_GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(output, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("docs/llms.txt is stale. Try: SPINUP_GOLDEN_UPDATE=1 go test ./internal/cli -run LLMS")
	}
	if !bytes.HasPrefix(got, []byte("# spinup\n\n> ")) {
		t.Fatal("docs/llms.txt has an invalid heading")
	}
	for _, line := range strings.Split(string(got), "\n") {
		start := strings.Index(line, "("+render.DocsBase)
		if start < 0 {
			continue
		}
		link := line[start+1+len(render.DocsBase):]
		link, _, _ = strings.Cut(link, ")")
		if _, err := os.Stat(filepath.Join(docs, filepath.FromSlash(link))); err != nil {
			t.Fatalf("docs/llms.txt links to a missing file %q: %v", link, err)
		}
	}
}
