// Package routes registers the user application routes.
package routes

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps contains the dependencies for route handlers.
type Deps struct {
	DB *pgxpool.Pool
}

// Register adds generated routes and application routes to mux.
func Register(mux *http.ServeMux, d Deps) {
	registerGenerated(mux, d)
	mux.HandleFunc("GET /custom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Add your application routes here. Generators never edit this file.
}
