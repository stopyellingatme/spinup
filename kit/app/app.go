package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/devtools"
	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/logx"
)

// App owns an HTTP handler, named commands, and lifecycle hooks.
// Construct an App with New. Handler supports concurrent requests.
// Call Run once for the server lifecycle.
type App struct {
	name            string
	version         string
	addr            string
	config          config.Base
	logger          *slog.Logger
	routes          []func(*http.ServeMux)
	middleware      []httpx.Middleware
	readiness       map[string]func(context.Context) error
	commands        map[string]func(context.Context, []string) error
	shutdown        []func(context.Context) error
	handler         http.Handler
	migrationStatus func(context.Context) ([]devtools.Migration, error)
	routeMu         sync.Mutex
	routePatterns   map[string]struct{}
}

// New constructs an API with the default middleware and built-in routes.
// The defaults are port 8080, a 20-second shutdown timeout, and JSON logging to stderr.
func New(opts ...Option) *App {
	a := &App{readiness: make(map[string]func(context.Context) error), commands: make(map[string]func(context.Context, []string) error), routePatterns: make(map[string]struct{})}
	for _, opt := range opts {
		opt(a)
	}
	if a.config.Port <= 0 {
		a.config.Port = 8080
	}
	if a.config.ShutdownTimeout <= 0 {
		a.config.ShutdownTimeout = 20 * time.Second
	}
	if a.addr == "" {
		a.addr = ":" + strconv.Itoa(a.config.Port)
	}
	if a.logger == nil {
		level := slog.LevelInfo
		if err := level.UnmarshalText([]byte(a.config.LogLevel)); err != nil {
			level = slog.LevelInfo
		}
		a.logger = logx.New(os.Stderr, logx.Options{Level: level})
	}
	mux := http.NewServeMux()
	a.addRoute("GET /healthz")
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_ = httpx.WriteJSON(w, http.StatusOK, struct {
			Status string `json:"status"`
		}{Status: "ok"})
	})
	mux.HandleFunc("GET /readyz", a.ready)
	a.addRoute("GET /readyz")
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		_ = httpx.WriteJSON(w, http.StatusOK, struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Go      string `json:"go"`
		}{Name: a.name, Version: a.version, Go: runtime.Version()})
	})
	a.addRoute("GET /version")
	for _, routes := range a.routes {
		routes(mux)
	}
	middleware := []httpx.Middleware{httpx.RequestID(), httpx.Recover(a.logger), httpx.AccessLog(a.logger), httpx.SecurityHeaders()}
	if a.config.Env == "dev" {
		dev := devtools.New(devtools.Options{Name: a.name, Version: a.version, Config: a.config, Routes: a.devRoutes, Migrations: a.migrationStatus})
		handler := dev.Handler()
		mux.Handle("/_dev", handler)
		mux.Handle("/_dev/", handler)
		a.addRoute("/_dev")
		a.addRoute("/_dev/")
		middleware = append(middleware, dev.Middleware(), a.recordRoute())
	}
	a.handler = httpx.Chain(mux, append(middleware, a.middleware...)...)
	return a
}

// Handler returns the complete HTTP handler, including the default middleware.
func (a *App) Handler() http.Handler { return a.handler }

func (a *App) addRoute(pattern string) {
	a.routeMu.Lock()
	defer a.routeMu.Unlock()
	a.routePatterns[pattern] = struct{}{}
}

func (a *App) recordRoute() httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			if r.Pattern != "" {
				a.addRoute(r.Pattern)
			}
		})
	}
}

func (a *App) devRoutes() []devtools.Route {
	a.routeMu.Lock()
	defer a.routeMu.Unlock()
	patterns := make([]string, 0, len(a.routePatterns))
	for pattern := range a.routePatterns {
		patterns = append(patterns, pattern)
	}
	slices.Sort(patterns)
	routes := make([]devtools.Route, len(patterns))
	for i, pattern := range patterns {
		routes[i] = devtools.Route{Pattern: pattern}
	}
	return routes
}

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	checks := make(map[string]string, len(a.readiness))
	status, result := http.StatusOK, "ok"
	for _, name := range sortedNames(a.readiness) {
		err := readinessCheck(r.Context(), a.readiness[name])
		checks[name] = "ok"
		if err != nil {
			status, result = http.StatusServiceUnavailable, "fail"
			checks[name] = "fail"
			a.logger.WarnContext(r.Context(), "readiness check failed", "check", name, "request_id", httpx.RequestIDFrom(r.Context()), "error", err)
		}
	}
	_ = httpx.WriteJSON(w, status, struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}{Status: result, Checks: checks})
}

func readinessCheck(parent context.Context, check func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	type outcome struct {
		err        error
		panicValue any
	}
	result := make(chan outcome, 1)
	go func() {
		value := outcome{}
		defer func() {
			value.panicValue = recover()
			result <- value
		}()
		value.err = check(ctx)
	}()
	select {
	case value := <-result:
		if value.panicValue != nil {
			panic(value.panicValue)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return value.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func sortedNames[V any](values map[string]V) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Run dispatches a named command, or serves HTTP when args is empty.
// Cancellation starts graceful shutdown. Hooks run after the server stops.
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) > 0 {
		if run, ok := a.commands[args[0]]; ok {
			return run(ctx, args[1:])
		}
		if args[0] == "healthcheck" {
			return a.healthcheck(ctx)
		}
		commands := strings.Join(sortedNames(a.commands), ", ")
		if commands == "" {
			commands = "(none)"
		}
		return fmt.Errorf("cannot run command %q: unknown command. Available commands: %s.\nTry: use a listed command, or omit the command to serve HTTP", args[0], commands)
	}
	return a.serve(ctx)
}

func (a *App) serve(ctx context.Context) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", a.addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w\nTry: select an available listen address and restart", a.addr, err)
	}
	return a.serveListener(ctx, listener)
}

func (a *App) serveListener(ctx context.Context, listener net.Listener) error {
	server := &http.Server{
		Handler:           a.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return logx.WithLogger(context.WithoutCancel(ctx), a.logger) },
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	a.logger.InfoContext(ctx, "HTTP server listening", "addr", listener.Addr().String())
	var problems []error
	stopped := false
	select {
	case <-ctx.Done():
	case err := <-done:
		stopped = true
		if !errors.Is(err, http.ErrServerClosed) {
			problems = append(problems, fmt.Errorf("HTTP server failed: %w", err))
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.config.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		problems = append(problems, fmt.Errorf("HTTP shutdown failed: %w", err))
		if closeErr := server.Close(); closeErr != nil {
			problems = append(problems, fmt.Errorf("HTTP close failed: %w", closeErr))
		}
	}
	// Shutdown can finish before the serving goroutine starts.
	// Wait for Serve to close its listener before running hooks.
	if !stopped {
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			problems = append(problems, fmt.Errorf("HTTP server failed: %w", err))
		}
	}
	for i := len(a.shutdown) - 1; i >= 0; i-- {
		if err := a.shutdown[i](shutdownCtx); err != nil {
			problems = append(problems, fmt.Errorf("shutdown hook failed: %w", err))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w\nTry: correct the reported server or shutdown failure and restart", errors.Join(problems...))
	}
	return nil
}

// Main runs a with process arguments and cancels it on SIGINT or SIGTERM.
// It logs errors and exits with status 1 when Run fails.
func Main(a *App) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := a.Run(ctx, os.Args[1:])
	stop()
	if err != nil {
		a.logger.Error("application failed", "error", err)
		os.Exit(1)
	}
}
