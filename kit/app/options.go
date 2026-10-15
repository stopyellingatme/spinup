package app

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

// Option configures an App during construction.
type Option func(*App)

// WithName sets the application name returned by /version.
func WithName(name string) Option { return func(a *App) { a.name = name } }

// WithVersion sets the application version returned by /version.
func WithVersion(version string) Option { return func(a *App) { a.version = version } }

// WithLogger sets the application logger. A nil logger selects the default logger.
func WithLogger(l *slog.Logger) Option { return func(a *App) { a.logger = l } }

// WithConfig sets the common settings. Nonpositive port and shutdown timeout use defaults.
// An empty or invalid log level uses info.
func WithConfig(cfg config.Base) Option { return func(a *App) { a.config = cfg } }

// WithRoutes adds a route registrar. Registrars run in option order during New.
func WithRoutes(routes func(mux *http.ServeMux)) Option {
	return func(a *App) { a.routes = append(a.routes, routes) }
}

// WithMiddleware adds middleware inside the default chain, in option order.
func WithMiddleware(middleware ...httpx.Middleware) Option {
	return func(a *App) { a.middleware = append(a.middleware, middleware...) }
}

// WithReadiness registers a named readiness check. A later registration replaces it.
// Checks must honor context cancellation and support concurrent requests.
func WithReadiness(name string, check func(ctx context.Context) error) Option {
	return func(a *App) { a.readiness[name] = check }
}

// WithCommand registers a named command. A later registration replaces it.
func WithCommand(name string, run func(ctx context.Context, args []string) error) Option {
	return func(a *App) { a.commands[name] = run }
}

// WithShutdown adds a shutdown hook. Hooks run in reverse option order after serving stops.
// Hooks share the server shutdown deadline and must honor context cancellation.
func WithShutdown(hook func(ctx context.Context) error) Option {
	return func(a *App) { a.shutdown = append(a.shutdown, hook) }
}

// WithAddr overrides the listen address. An empty address uses the configured port.
func WithAddr(addr string) Option { return func(a *App) { a.addr = addr } }
