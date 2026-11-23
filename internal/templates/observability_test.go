package templates_test

import (
	"bytes"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

// TestMonitoringTraceReceiver checks trace acceptance in lite and preserves full output.
func TestMonitoringTraceReceiver(t *testing.T) {
	t.Parallel()
	const path = "monitoring/alloy/config.alloy"
	lite := readDeployFile(t, renderMonitoring(t, "lite"), path)
	// A receiver with no trace consumer does not register the OTLP trace endpoint.
	for _, want := range []string{
		"traces  = [otelcol.processor.filter.discard_traces.input]",
		`otelcol.processor.filter "discard_traces" {`,
		`span = ["true"]`,
		"traces = []",
	} {
		if !strings.Contains(lite, want) {
			t.Errorf("lite trace receiver is missing %q", want)
		}
	}
	if strings.Contains(lite, "http://tempo:4318") {
		t.Error("lite must discard traces without exporting them to Tempo")
	}
	full := readDeployFile(t, renderMonitoring(t, "full"), path)
	want, err := os.ReadFile("../../testdata/golden/monitoring/full/" + path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(full), want) {
		t.Error("full trace pipeline changed")
	}
	if strings.Contains(full, "discard_traces") {
		t.Error("full must export traces instead of discarding them")
	}
}

// TestJobsTelemetry checks worker telemetry across the supported module combinations.
func TestJobsTelemetry(t *testing.T) {
	t.Parallel()
	for _, modules := range []string{"jobs", "jobs,mail", "auth,jobs,billing", "auth,jobs,billing,apikeys"} {
		t.Run(modules, func(t *testing.T) {
			t.Parallel()
			packs := append([]string{"base", "api"}, strings.Split(modules, ",")...)
			tree := testutil.RenderPack(t, packs, map[string]any{
				"app_name": "todo", "module": "example.com/todo", "modules": modules,
			})
			data, err := fs.ReadFile(tree, "api/cmd/worker/main.go")
			if err != nil {
				t.Fatal(err)
			}
			worker := string(data)
			for _, want := range []string{
				`telemetry.Setup(ctx, telemetry.Options{ServiceName: "todo-worker", ServiceVersion: version, Environment: cfg.Env})`,
				"func run() (err error)",
				"context.WithTimeout(context.Background(), cfg.ShutdownTimeout)",
				"err = errors.Join(err, shutdown(shutdownCtx))",
				"logx.New(os.Stderr, logx.Options{Level: level})",
				"if telemetry.Enabled() {\n\t\tlogger = slog.New(telemetry.SlogHandler(logger.Handler()))\n\t\tslog.SetDefault(logger)\n\t}",
				"jobs.WithLogger(logger)",
			} {
				if !strings.Contains(worker, want) {
					t.Errorf("worker telemetry is missing %q", want)
				}
			}
			setup := strings.Index(worker, "telemetry.Setup(")
			logger := strings.Index(worker, "logx.New(")
			pool := strings.Index(worker, "db.Open(")
			if setup < 0 || setup > logger || setup > pool {
				t.Error("telemetry must start before the logger and database pool")
			}
		})
	}
}
