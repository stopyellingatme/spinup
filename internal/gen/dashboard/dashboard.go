// Package dashboard generates custom Grafana dashboards.
package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

const dashboardDir = "monitoring/grafana/dashboards/custom"

var dashboardName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,32}$`)

func init() {
	registry.Register(gen.Generator{
		Name:  "dashboard",
		Short: "Add a custom Grafana dashboard",
		Usage: "<name>",
		Flags: flags,
		Run:   plan,
	})
}

func flags(fs *pflag.FlagSet) {
	fs.StringArray("panel", nil, "time-series panel as title=PromQL expression")
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 1 {
		return nil, fmt.Errorf("expected one dashboard name\nTry: spinup g dashboard service-health")
	}
	if in.Lock == nil || !slices.Contains(in.Lock.Packs, "monitoring") {
		return nil, fmt.Errorf("the app has no monitoring pack\nTry: create the app with --modules ...,monitoring")
	}
	name := in.Args[0]
	if !dashboardName.MatchString(name) {
		return nil, fmt.Errorf("invalid dashboard name %q: use lower-case letters, digits, and hyphens\nTry: spinup g dashboard service-health", name)
	}
	panels := []panel{{Title: "Request rate", Expr: `sum(rate(http_server_request_duration_seconds_count[$__rate_interval]))`}}
	if in.Flags != nil {
		values, err := in.Flags.GetStringArray("panel")
		if err != nil {
			return nil, fmt.Errorf("read dashboard panels: %w\nTry: pass --panel title=expression", err)
		}
		if len(values) > 0 {
			panels = make([]panel, 0, len(values))
			for _, value := range values {
				item, err := parsePanel(value)
				if err != nil {
					return nil, err
				}
				panels = append(panels, item)
			}
		}
	}
	data, err := json.MarshalIndent(newDashboard(name, panels), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode dashboard: %w\nTry: use a valid dashboard name and panel", err)
	}
	data = append(data, '\n')
	path := dashboardDir + "/" + name + ".json"
	return &gen.Output{Plan: vfs.Plan{Files: []vfs.File{{Path: path, Data: data, Class: string(manifest.Seed)}}}, Next: []string{"spinup monitor dashboards sync"}}, nil
}

type panel struct {
	Title string
	Expr  string
}

func parsePanel(value string) (panel, error) {
	title, expr, ok := strings.Cut(value, "=")
	title = strings.TrimSpace(title)
	expr = strings.TrimSpace(expr)
	if !ok || title == "" || expr == "" {
		return panel{}, fmt.Errorf("invalid panel %q: use title=expr (a PromQL expression)\nTry: spinup g dashboard service-health --panel 'Request rate=sum(rate(http_server_request_duration_seconds_count[5m]))'", value)
	}
	return panel{Title: title, Expr: expr}, nil
}

func newDashboard(name string, panels []panel) map[string]any {
	items := make([]any, 0, len(panels))
	for i, item := range panels {
		items = append(items, map[string]any{
			"datasource":  map[string]string{"type": "prometheus", "uid": "prometheus"},
			"fieldConfig": map[string]any{"defaults": map[string]any{"custom": map[string]any{"drawStyle": "line", "fillOpacity": 10, "lineWidth": 1, "showPoints": "never"}, "unit": "short"}, "overrides": []any{}},
			"gridPos":     map[string]int{"h": 8, "w": 12, "x": (i % 2) * 12, "y": (i / 2) * 8},
			"id":          i + 1,
			"targets":     []any{map[string]any{"datasource": map[string]string{"type": "prometheus", "uid": "prometheus"}, "expr": item.Expr, "legendFormat": "{{job}}", "refId": "A"}},
			"title":       item.Title,
			"type":        "timeseries",
		})
	}
	return map[string]any{
		"editable":      true,
		"panels":        items,
		"schemaVersion": 39,
		"tags":          []string{"spinup", "custom"},
		"time":          map[string]string{"from": "now-6h", "to": "now"},
		"timezone":      "",
		"title":         strings.ReplaceAll(name, "-", " "),
		"uid":           "spinup-" + name,
		"version":       1,
	}
}
