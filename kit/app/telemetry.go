package app

import (
	"log/slog"
	"net/http"

	"github.com/stopyellingatme/spinup/kit/logx"
	"github.com/stopyellingatme/spinup/kit/telemetry"
)

func (a *App) telemetryLogger() {
	if telemetry.Enabled() {
		a.logger = slog.New(telemetry.SlogHandler(a.logger.Handler()))
	}
}

func (a *App) telemetryHandler(next http.Handler, mux *http.ServeMux) http.Handler {
	if !telemetry.Enabled() {
		return next
	}
	traced := telemetry.HTTPMiddleware(a.name)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(logx.WithLogger(r.Context(), a.logger)))
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Resolve the bounded pattern before middleware copies the request.
		// ServeMux still performs dispatch and sets wildcard values itself.
		_, r.Pattern = mux.Handler(r)
		traced.ServeHTTP(w, r)
	})
}
