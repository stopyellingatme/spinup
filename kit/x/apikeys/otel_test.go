package apikeys

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestRequestCounter(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); _ = provider.Shutdown(t.Context()) })
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	req := testRequest()
	req.Plan = Plan{PerMinute: 1, PerDay: 1}
	s.days[userDay{1, utcDay(now)}] = &dayUsage{}
	h := s.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, path := range []string{"/api/admin/keys", "/api/notes", "/api/notes"} {
		r := httptest.NewRequestWithContext(requestContext(t.Context(), req), "GET", path, nil)
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	now = now.Add(time.Minute)
	r := httptest.NewRequestWithContext(requestContext(t.Context(), req), "GET", "/api/notes", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]int64{}
	for _, scope := range data.ScopeMetrics {
		if scope.Scope.Name != "github.com/stopyellingatme/spinup/kit/x/apikeys" {
			continue
		}
		for _, m := range scope.Metrics {
			if m.Name != "apikeys.requests" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("counter type = %T", m.Data)
			}
			for _, point := range sum.DataPoints {
				plan, _ := point.Attributes.Value(attribute.Key("plan"))
				if plan.AsString() != "free" {
					t.Fatalf("plan = %v", plan)
				}
				outcome, _ := point.Attributes.Value(attribute.Key("outcome"))
				outcomes[outcome.AsString()] += point.Value
			}
		}
	}
	for _, outcome := range []string{"ok", "forbidden", "minute_limit", "day_limit"} {
		if outcomes[outcome] != 1 {
			t.Fatalf("outcomes = %v", outcomes)
		}
	}
	if len(outcomes) != 4 {
		t.Fatal(outcomes)
	}
}
