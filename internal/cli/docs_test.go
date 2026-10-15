package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/docsite"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates/store"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestDocsCompat(t *testing.T) {
	t.Parallel()
	code, out, errOut := run(t, "docs", "compat", "--json")
	if code != ui.ExitOK || !strings.Contains(out, `"postgresql_majors"`) || errOut != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func TestDocsReference(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	code, out, errOut := run(t, "docs", "reference", "--out", dir)
	if code != ui.ExitOK || out != "" || errOut != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
	data, err := os.ReadFile(filepath.Join(dir, "commands", "docs-compat.md"))
	if err != nil || !strings.Contains(string(data), "# spinup docs compat") {
		t.Fatalf("reference page = %q, %v", data, err)
	}
}

func TestDocsSiteIsCurrent(t *testing.T) {
	var entered bool
	app := &App{Flags: &GlobalFlags{}, UI: ui.New(nil, &bytes.Buffer{}, &bytes.Buffer{}, ui.Options{})}
	root := newRoot(app, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, &entered)
	matrix := docsite.NewCompatibility(Version, []string{store.Current(Version).Version}, stackversions.MustLoad())
	pages := docsite.SitePages(root, matrix)
	site := filepath.Join(repoRoot(t), "docs", "site")
	if os.Getenv("SPINUP_GOLDEN_UPDATE") == "1" {
		if err := writePages(site, pages); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := pagesMatch(site, pages)
	if err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Fatal("docs site pages are stale. Try: SPINUP_GOLDEN_UPDATE=1 go test ./internal/cli -run TestDocsSiteIsCurrent")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatal("repository root was not found")
		}
		dir = next
	}
}
