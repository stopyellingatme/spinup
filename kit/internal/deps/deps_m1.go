//go:build deps

// Package deps pins the modules that a milestone needs before any kit code imports them,
// so that `go mod tidy` keeps them. Main loop only (see CLAUDE.md).
package deps

// M1 pre-seed.
import (
	_ "github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/pressly/goose/v3"
	_ "github.com/testcontainers/testcontainers-go"
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"
)
