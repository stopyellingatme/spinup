package app

import (
	"net/http"

	"github.com/stopyellingatme/spinup/kit/deployx"
)

// WithDeployInfo mounts the protected deployment endpoint with the application version.
func WithDeployInfo(options deployx.Options) Option {
	return func(a *App) {
		a.routes = append(a.routes, func(mux *http.ServeMux) {
			infoOptions := options
			infoOptions.Version = a.version
			mux.Handle("GET /_deploy/info", deployx.New(infoOptions))
			a.addRoute("GET /_deploy/info")
		})
	}
}
