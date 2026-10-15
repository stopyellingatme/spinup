package alert

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestPlanPreservesCommentAndRejectsDuplicate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(customRulesPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Keep this comment\ngroups:\n  - name: custom\n    rules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := gen.Input{Root: root, Args: []string{"HighErrors"}, Flags: testFlags(t), Lock: lock()}
	out, err := plan(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.Edits, []string{customRulesPath}; !slices.Equal(got, want) {
		t.Fatalf("edits = %v, want %v", got, want)
	}
	file, _ := out.Plan.Get(customRulesPath)
	text := string(file.Data)
	for _, want := range []string{"# Keep this comment", "alert: HighErrors", "for: 5m", "severity: warning", "summary: Too many errors"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rule lacks %q:\n%s", want, text)
		}
	}
	if err := os.WriteFile(path, file.Data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := plan(context.Background(), in); err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestPlanRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   gen.Input
		want string
	}{
		{"no pack", gen.Input{Args: []string{"HighErrors"}, Flags: testFlags(t), Lock: manifest.New()}, "no monitoring pack"},
		{"missing expr", gen.Input{Args: []string{"HighErrors"}, Flags: emptyFlags(t), Lock: lock()}, "--expr is required"},
		{"bad severity", gen.Input{Args: []string{"HighErrors"}, Flags: flagsFor(t, "--expr", "up == 0", "--severity", "low"), Lock: lock()}, "severity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := plan(context.Background(), tt.in)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func lock() *manifest.Lock {
	lock := manifest.New()
	lock.Packs = []string{"base", "api", "monitoring"}
	return lock
}

func testFlags(t *testing.T) *pflag.FlagSet {
	t.Helper()
	return flagsFor(t, "--expr", "sum(rate(errors_total[5m])) > 0", "--for", "5m", "--severity", "warning", "--summary", "Too many errors")
}

func emptyFlags(t *testing.T) *pflag.FlagSet { return flagsFor(t) }

func flagsFor(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("alert", pflag.ContinueOnError)
	flags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return fs
}
