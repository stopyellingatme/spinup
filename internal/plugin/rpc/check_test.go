package rpc

import (
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

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
