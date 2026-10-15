package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Execute(context.Background(), args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestNoArgsCard(t *testing.T) {
	t.Parallel()
	code, out, _ := run(t)
	if code != ui.ExitOK {
		t.Fatalf("code = %d", code)
	}
	for _, s := range []string{"spinup new <name>", "spinup dev", "spinup --help"} {
		if !strings.Contains(out, s) {
			t.Errorf("card misses %q: %s", s, out)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		try  string
	}{
		{"unknown command", []string{"nope"}, "Try: spinup --help"},
		{"unknown flag", []string{"--nope"}, "Try: spinup --help"},
		{"unknown subcommand flag", []string{"version", "--nope"}, "Try: spinup version --help"},
		{"extra argument", []string{"version", "extra"}, "Try: spinup version --help"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, out, errOut := run(t, tc.args...)
			if code != ui.ExitUsage {
				t.Fatalf("code = %d, want %d (%s)", code, ui.ExitUsage, errOut)
			}
			if out != "" {
				t.Errorf("stdout = %q", out)
			}
			if !strings.Contains(errOut, tc.try) || !strings.HasPrefix(errOut, "Error: ") {
				t.Errorf("stderr = %q", errOut)
			}
		})
	}
}

func TestUsageErrorJSON(t *testing.T) {
	t.Parallel()
	code, _, errOut := run(t, "--json", "nope")
	if code != ui.ExitUsage {
		t.Fatalf("code = %d", code)
	}
	var got struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(errOut), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", errOut, err)
	}
	if got.Error["code"] != float64(ui.ExitUsage) || got.Error["try"] != "spinup --help" {
		t.Errorf("got %v", got.Error)
	}
}

func TestVersion(t *testing.T) {
	t.Parallel()
	code, out, _ := run(t, "version")
	if code != 0 || !strings.HasPrefix(out, "spinup ") || !strings.Contains(out, "(commit ") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestVersionJSON(t *testing.T) {
	t.Parallel()
	code, out, _ := run(t, "version", "--json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	for _, k := range []string{"version", "commit", "date", "go", "kit"} {
		if m[k] == "" {
			t.Errorf("key %q missing in %v", k, m)
		}
	}
	if len(m) != 5 || m["kit"] != m["version"] {
		t.Errorf("got %v", m)
	}
}

func TestDeterministicCommandOrder(t *testing.T) {
	// Not parallel: it changes the registry.
	regMu.Lock()
	saved := registry
	registry = nil
	regMu.Unlock()
	t.Cleanup(func() {
		regMu.Lock()
		registry = saved
		regMu.Unlock()
	})
	for _, name := range []string{"zeta", "alpha", "mid", "beta"} {
		Register(GroupDevelop, func(*App) *cobra.Command {
			return &cobra.Command{Use: name, Run: func(*cobra.Command, []string) {}}
		})
	}
	names := func() string {
		var entered bool
		app := &App{Flags: &GlobalFlags{}, UI: ui.New(nil, &bytes.Buffer{}, &bytes.Buffer{}, ui.Options{})}
		root := newRoot(app, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, &entered)
		var n []string
		for _, c := range root.Commands() {
			n = append(n, c.Name())
		}
		return strings.Join(n, ",")
	}
	first := names()
	for range 5 {
		if got := names(); got != first {
			t.Fatalf("order changed: %q vs %q", got, first)
		}
	}
	for _, want := range []string{"alpha,beta,mid,zeta"} {
		if !strings.Contains(first, want) {
			t.Errorf("order = %q, want it to contain %q", first, want)
		}
	}
}

func TestVersionRegisteredInMaintain(t *testing.T) {
	t.Parallel()
	var entered bool
	app := &App{Flags: &GlobalFlags{}, UI: ui.New(nil, &bytes.Buffer{}, &bytes.Buffer{}, ui.Options{})}
	root := newRoot(app, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, &entered)
	c, _, err := root.Find([]string{"version"})
	if err != nil || c.GroupID != string(GroupMaintain) {
		t.Fatalf("c=%v err=%v", c, err)
	}
}
