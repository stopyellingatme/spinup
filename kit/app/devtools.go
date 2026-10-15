package app

import (
	"context"

	"github.com/stopyellingatme/spinup/kit/devtools"
)

// WithMigrationStatus supplies the migration state for the development page.
func WithMigrationStatus(status func(context.Context) ([]devtools.Migration, error)) Option {
	return func(a *App) { a.migrationStatus = status }
}
