package rpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

func TestProjectChecksWithoutPlugins(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("[app]\nname='demo'\nmodule='example.com/demo'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runner := wireRunner{func(_ context.Context, _ execx.Cmd) (execx.Result, error) {
		t.Error("doctor started a plugin without an exec plugin declaration")
		return execx.Result{}, nil
	}}
	checks, err := ProjectChecks(t.Context(), Options{Root: root, Runner: runner, Offline: true})
	if err != nil || len(checks) != 0 {
		t.Fatalf("checks=%v err=%v", checks, err)
	}
}

func TestCheckFindings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		statuses []string
		want     doctor.Status
	}{
		{"empty", nil, doctor.OK}, {"skip", []string{"skip"}, doctor.Skip},
		{"ok and skip", []string{"ok", "skip"}, doctor.OK},
		{"warn", []string{"warn", "ok"}, doctor.Warn},
		{"fail", []string{"warn", "fail", "skip"}, doctor.Fail},
		{"invalid", []string{"unknown"}, doctor.Fail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var findings []sdk.Finding
			for _, status := range tc.statuses {
				findings = append(findings, sdk.Finding{Status: status, Message: "message", Try: "repair"})
			}
			got := checkResult(sdk.CheckResult{Findings: findings})
			if got.Status != tc.want {
				t.Fatalf("%+v, want %s", got, tc.want)
			}
		})
	}
}
