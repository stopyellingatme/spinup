package db

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/telemetry"
)

func configureTelemetry(cfg *pgxpool.Config) { telemetry.ConfigurePool(cfg) }
