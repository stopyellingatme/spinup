package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestPinNotice(t *testing.T) {
	t.Setenv("CI", "")
	if err := os.Unsetenv("CI"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPINUP_NO_PIN_NOTICE", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, cli, pin, command string
		json, ci, disabled      bool
		want                    string
	}{
		{"older", "0.1.0-alpha.700", "0.1.0-alpha.830", "check", false, false, false, "Warning: spinup 0.1.0-alpha.700 is older than 0.1.0-alpha.830, the version that wrote spinup.lock. Try: spinup self-update\n"},
		{"newer", "0.1.0-alpha.830", "0.1.0-alpha.700", "check", false, false, false, "Warning: spinup 0.1.0-alpha.830 is newer than 0.1.0-alpha.700, the version that wrote spinup.lock. Try: spinup upgrade --plan\n"},
		{"equal", "0.1.0-alpha.830", "0.1.0-alpha.830", "check", false, false, false, ""},
		{"development", "dev", "0.1.0-alpha.830", "check", false, false, false, ""},
		{"json", "0.1.0-alpha.700", "0.1.0-alpha.830", "check", true, false, false, ""},
		{"CI", "0.1.0-alpha.700", "0.1.0-alpha.830", "check", false, true, false, ""},
		{"disabled", "0.1.0-alpha.700", "0.1.0-alpha.830", "check", false, false, true, ""},
		{"new", "0.1.0-alpha.700", "0.1.0-alpha.830", "new", false, false, false, ""},
		{"doctor", "0.1.0-alpha.700", "0.1.0-alpha.830", "doctor", false, false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CI", "")
			if err := os.Unsetenv("CI"); err != nil {
				t.Fatal(err)
			}
			if tc.ci {
				t.Setenv("CI", "1")
			}
			if tc.disabled {
				t.Setenv("SPINUP_NO_PIN_NOTICE", "1")
			}
			if err := os.WriteFile(filepath.Join(root, "spinup.lock"), []byte(`{"cli_version":"`+tc.pin+`"}`), 0644); err != nil {
				t.Fatal(err)
			}
			previous := Version
			Version = tc.cli
			t.Cleanup(func() { Version = previous })
			app := &App{Flags: &GlobalFlags{Dir: root, JSON: tc.json}, UI: ui.New(nil, nil, nil, ui.Options{})}
			command := &cobra.Command{Use: tc.command}
			var stderr bytes.Buffer
			printPinNotice(command, app, &stderr)
			if stderr.String() != tc.want {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestPinNoticeOutsideProjectAndBadLock(t *testing.T) {
	t.Setenv("CI", "")
	if err := os.Unsetenv("CI"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPINUP_NO_PIN_NOTICE", "")
	previous := Version
	Version = "0.1.0-alpha.700"
	t.Cleanup(func() { Version = previous })
	for _, tc := range []struct {
		name, lock string
		project    bool
	}{
		{"outside project", "", false},
		{"bad lock", "{", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.project {
				if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(""), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "spinup.lock"), []byte(tc.lock), 0644); err != nil {
					t.Fatal(err)
				}
			}
			app := &App{Flags: &GlobalFlags{Dir: root}, UI: ui.New(nil, nil, nil, ui.Options{})}
			var stderr bytes.Buffer
			printPinNotice(&cobra.Command{Use: "run"}, app, &stderr)
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}
