// Package live registers the live data feeds of the app (ADR 0033).
//
// Add your own feeds in this package. A feed file has the marker `// spinup:live-feed <feed> register<Name>Feed`.
package live

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	kitlive "github.com/stopyellingatme/spinup/kit/x/live"
)

// Deps holds what the feed routes need.
type Deps struct{ DB *pgxpool.Pool }

// Routes returns the routes of every feed. Pass it to app.WithRoutes.
func Routes(h *kitlive.Hub, d Deps) func(*http.ServeMux) {
	return func(mux *http.ServeMux) { registerGenerated(mux, h, d) }
}
