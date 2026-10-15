package telemetry

import (
	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConfigurePool adds pgx tracing when telemetry is enabled.
// SQL text and query parameters are omitted to protect application data.
func ConfigurePool(cfg *pgxpool.Config) {
	if Enabled() {
		cfg.ConnConfig.Tracer = otelpgx.NewTracer(otelpgx.WithDisableSQLStatementInAttributes())
	}
}
