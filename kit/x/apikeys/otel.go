package apikeys

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func (s *Store) initCounter() {
	if counter, err := otel.Meter("github.com/stopyellingatme/spinup/kit/x/apikeys").Int64Counter("apikeys.requests", metric.WithDescription("API key requests by plan and outcome.")); err == nil {
		s.counter = counter
	}
}
func (s *Store) record(ctx context.Context, plan, outcome string) {
	if s.counter != nil {
		s.counter.Add(ctx, 1, metric.WithAttributes(attribute.String("plan", plan), attribute.String("outcome", outcome)))
	}
}
