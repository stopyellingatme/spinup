// Package jobs holds the background jobs of the app. Add a job with
// `spinup g job <name>`. The worker process (cmd/worker) runs them.
package jobs

import "github.com/jackc/pgx/v5/pgxpool"

// Deps holds what the jobs need. Add fields here, and set them in
// cmd/worker/main.go.
type Deps struct {
	DB *pgxpool.Pool
}
