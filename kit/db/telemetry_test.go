package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/telemetry"
)

func TestPoolTelemetry(t *testing.T) {
	for _, on := range []bool{false, true} {
		name := "disabled"
		if on {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			endpoint := ""
			if on {
				endpoint = "http://127.0.0.1:1"
			}
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
			stop, err := telemetry.Setup(context.Background(), telemetry.Options{ServiceName: "test-api"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
					t.Error(err)
				}
			})
			pool, err := db.Open(context.Background(), "postgres://app:pw@127.0.0.1:1/app?sslmode=disable")
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if got := pool.Config().ConnConfig.Tracer != nil; got != on {
				t.Fatalf("tracing = %t, want %t", got, on)
			}
		})
	}
}
