package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestPlan(t *testing.T) {
	t.Parallel()
	fs := pflag.NewFlagSet("dashboard", pflag.ContinueOnError)
	flags(fs)
	if err := fs.Parse([]string{"--panel", "Errors=sum(rate(errors_total[5m]))"}); err != nil {
		t.Fatal(err)
	}
	out, err := plan(context.Background(), gen.Input{Args: []string{"service-health"}, Flags: fs, Lock: monitoringLock()})
	if err != nil {
		t.Fatal(err)
	}
	file, ok := out.Plan.Get("monitoring/grafana/dashboards/custom/service-health.json")
	if !ok || file.Class != string(manifest.Seed) {
		t.Fatalf("plan = %#v", out.Plan)
	}
	var dashboard struct {
		UID    string `json:"uid"`
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(file.Data, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.UID != "spinup-service-health" || len(dashboard.Panels) != 1 || dashboard.Panels[0].Title != "Errors" || dashboard.Panels[0].Targets[0].Expr != "sum(rate(errors_total[5m]))" {
		t.Fatalf("dashboard = %#v", dashboard)
	}
}

func TestPlanDefaultAndRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   gen.Input
		want string
	}{
		{"default panel", gen.Input{Args: []string{"traffic"}, Lock: monitoringLock()}, "http_server_request_duration_seconds_count"},
		{"no pack", gen.Input{Args: []string{"traffic"}, Lock: manifest.New()}, "no monitoring pack"},
		{"bad name", gen.Input{Args: []string{"Traffic"}, Lock: monitoringLock()}, "invalid dashboard name"},
		{"bad panel", gen.Input{Args: []string{"traffic"}, Flags: panelFlags(t, "bad"), Lock: monitoringLock()}, "title=expr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := plan(context.Background(), tt.in)
			if tt.name == "default panel" {
				file, _ := out.Plan.Get("monitoring/grafana/dashboards/custom/traffic.json")
				if err != nil || !strings.Contains(string(file.Data), tt.want) {
					t.Fatalf("plan = %v, %v", out, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func monitoringLock() *manifest.Lock {
	lock := manifest.New()
	lock.Packs = []string{"base", "api", "monitoring"}
	return lock
}

func panelFlags(t *testing.T, values ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("dashboard", pflag.ContinueOnError)
	flags(fs)
	for _, value := range values {
		if err := fs.Set("panel", value); err != nil {
			t.Fatal(err)
		}
	}
	return fs
}
