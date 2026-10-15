package docsite

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/stackversions"
)

func TestCompatibility(t *testing.T) {
	t.Parallel()
	matrix := NewCompatibility("v1.2.3", []string{"v1.2.3", "v1.2.2", "v1.2.1", "v1.2.0"}, stackversions.MustLoad())
	if got, want := strings.Join(matrix.TemplateVersions, ","), "v1.2.3,v1.2.2,v1.2.1"; got != want {
		t.Fatalf("TemplateVersions = %q, want %q", got, want)
	}
	if !strings.Contains(matrix.Markdown(), "PostgreSQL majors: 14, 15, 16, 17, 18") {
		t.Fatal("Markdown does not contain the PostgreSQL policy")
	}
	data, err := matrix.JSON()
	if err != nil || !strings.Contains(string(data), `"cli_version": "v1.2.3"`) {
		t.Fatalf("JSON() = %q, %v", data, err)
	}
}

func TestReferencePages(t *testing.T) {
	t.Parallel()
	root := &cobra.Command{Use: "spinup", Short: "Root"}
	root.PersistentFlags().Bool("json", false, "write JSON")
	child := &cobra.Command{Use: "docs", Short: "Generate docs", Example: "spinup docs reference --out docs/site"}
	child.Flags().String("out", "", "write pages")
	child.AddCommand(&cobra.Command{Use: "compat", Short: "Print compatibility"})
	root.AddCommand(child)
	pages := ReferencePages(root)
	page := string(pages["commands/docs.md"])
	for _, want := range []string{"# spinup docs", "--json", "--out", "docs-compat.md", "## Examples"} {
		if !strings.Contains(page, want) {
			t.Errorf("docs page does not contain %q:\n%s", want, page)
		}
	}
	if _, ok := pages["commands/index.md"]; !ok {
		t.Fatal("command index is missing")
	}
}
