package alert

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	exec "github.com/stopyellingatme/spinup/internal/exec"
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
		{"bad duration", gen.Input{Args: []string{"HighErrors"}, Flags: flagsFor(t, "--expr", "up == 0", "--for", "banana"), Lock: lock()}, "--for"},
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

func TestRuleDuration(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		value string
		valid bool
	}{
		{"0", true}, {"0s", true}, {"5m", true}, {"1y2w3d4h5m6s7ms", true}, {"250ms", true},
		{"", false}, {"banana", false}, {"-1m", false}, {"1.5m", false}, {"1m1h", false},
		{"1h1h", false}, {"5", false}, {"1us", false}, {" 5m", false}, {"5m ", false},
		{"999999999999999999999999y", false}, {"293y", false},
		{"9223372036854ms", true}, {"9223372036855ms", false},
		{"9223372036s855ms", false},
	} {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			_, err := inputRule(flagsFor(t, "--expr", "up == 0", "--for", tt.value))
			if (err == nil) != tt.valid {
				t.Fatalf("duration %q error = %v, valid = %v", tt.value, err, tt.valid)
			}
			if err != nil && (!strings.Contains(err.Error(), "--for") || !strings.Contains(err.Error(), "Try:")) {
				t.Fatalf("duration error lacks guidance: %v", err)
			}
		})
	}
}

func TestPlanValidatesBeforeReturning(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("promtool"); err != nil {
		t.Skip("promtool is required")
	}
	for _, tt := range []struct{ name, expr string }{
		{"syntax error", "up =="},
		{"unknown function", "not_a_function(up)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(customRulesPath))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			original := "# Keep this comment\ngroups: []\n"
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := plan(context.Background(), gen.Input{Root: root, Args: []string{"HighErrors"}, Flags: flagsFor(t, "--expr", tt.expr), Lock: lock()})
			if err == nil || out != nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("invalid alert returned a plan: output = %v, error = %v", out, err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != original {
				t.Fatalf("seed changed: %q, error = %v", data, err)
			}
		})
	}
}

func TestPlanPromtoolAvailability(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the promtool stub requires a POSIX shell")
	}
	for _, tt := range []struct {
		name      string
		available bool
		exit      string
	}{
		{"valid rules", true, "0"},
		{"invalid rules", true, "1"},
		{"unavailable", false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			bin := t.TempDir()
			t.Setenv("PATH", bin)
			capturePath := filepath.Join(root, "validation-path")
			captureData := filepath.Join(root, "validation-data")
			t.Setenv("SPINUP_ALERT_TEMP_PATH", capturePath)
			t.Setenv("SPINUP_ALERT_TEMP_DATA", captureData)
			t.Setenv("SPINUP_ALERT_CHECK_EXIT", tt.exit)
			if tt.available {
				script := "#!/bin/sh\n[ \"$1\" = check ] && [ \"$2\" = rules ] || exit 2\nprintf '%s' \"$3\" > \"$SPINUP_ALERT_TEMP_PATH\"\n/bin/cat \"$3\" > \"$SPINUP_ALERT_TEMP_DATA\"\nexit \"$SPINUP_ALERT_CHECK_EXIT\"\n"
				if err := exec.WriteExecutable(filepath.Join(bin, "promtool"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, filepath.FromSlash(customRulesPath))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			original := "# Keep this comment\ngroups: []\n"
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			in := gen.Input{Root: root, Args: []string{"HighErrors"}, Flags: testFlags(t), Lock: lock()}
			out, err := plan(context.Background(), in)
			switch {
			case tt.exit == "1":
				if err == nil || out != nil || !strings.Contains(err.Error(), "before writing") {
					t.Fatalf("validation must reject the plan: %v, %v", out, err)
				}
			case err != nil:
				t.Fatal(err)
			case tt.available:
				if len(out.Commands) != 1 || !slices.Equal(out.Commands[0].Argv, []string{"promtool", "check", "rules", customRulesPath}) {
					t.Fatalf("final check missing: %v", out.Commands)
				}
			default:
				if len(out.Commands) != 0 || !strings.Contains(strings.Join(out.Next, "\n"), "promtool is unavailable") {
					t.Fatalf("missing unavailable note: %v", out)
				}
				in.Flags = flagsFor(t, "--expr", "up == 0", "--for", "banana")
				if out, err := plan(context.Background(), in); err == nil || out != nil || !strings.Contains(err.Error(), "invalid --for duration") {
					t.Fatalf("invalid duration accepted without promtool: %v, %v", out, err)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != original {
				t.Fatalf("seed changed: %q, error = %v", data, err)
			}
			if tt.available {
				tempPath, err := os.ReadFile(capturePath)
				if err != nil || string(tempPath) == path {
					t.Fatalf("validation must use a temporary copy: %q, %v", tempPath, err)
				}
				if _, err := os.Stat(string(tempPath)); !os.IsNotExist(err) {
					t.Fatalf("validation copy remains: %v", err)
				}
				data, err := os.ReadFile(captureData)
				if err != nil || !strings.Contains(string(data), "alert: HighErrors") || !strings.Contains(string(data), "# Keep this comment") {
					t.Fatalf("validation omitted new content: %q, %v", data, err)
				}
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
