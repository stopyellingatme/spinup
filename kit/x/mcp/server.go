package mcp

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Path is the HTTP endpoint for MCP requests.
const Path = "/api/mcp"

// MetadataPath is the endpoint for the protected resource metadata.
const MetadataPath = "/.well-known/oauth-protected-resource" + Path

// Config describes the server and its request and result limits.
type Config struct {
	// Name identifies the implementation. It is required.
	Name string
	// Version identifies the implementation version. It is required.
	Version string
	// Title is an optional display name.
	Title string
	// Instructions gives the model optional server instructions.
	Instructions string
	// MaxBodyBytes limits request bodies. Zero selects 1 MiB.
	MaxBodyBytes int64
	// MaxResultBytes limits the JSON form of each tool result. Zero selects 256 KiB.
	MaxResultBytes int64
}

// Option configures a Server.
type Option func(*options)

type options struct {
	logger *slog.Logger
	meter  metric.MeterProvider
	tracer trace.TracerProvider
}

// WithLogger selects the logger. The default is slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}

// WithMeterProvider selects the meter provider. The default is otel.GetMeterProvider().
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(o *options) {
		if mp != nil {
			o.meter = mp
		}
	}
}

// WithTracerProvider selects the tracer provider. The default is otel.GetTracerProvider().
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(o *options) {
		if tp != nil {
			o.tracer = tp
		}
	}
}

// Server serves typed tools with the API key and role checks.
type Server struct {
	cfg      Config
	sdk      *sdkmcp.Server
	logger   *slog.Logger
	tracer   trace.Tracer
	duration metric.Float64Histogram
	mu       sync.RWMutex
	tools    map[string]ToolDef
}

// New validates cfg and creates a server without I/O.
func New(cfg Config, opts ...Option) (*Server, error) {
	const hint = "\nTry: correct the configuration passed to mcp.New."
	switch {
	case strings.TrimSpace(cfg.Name) == "":
		return nil, fmt.Errorf("mcp: the implementation name is empty.%s", hint)
	case strings.TrimSpace(cfg.Version) == "":
		return nil, fmt.Errorf("mcp: the implementation version is empty.%s", hint)
	case cfg.MaxBodyBytes < 0 || cfg.MaxResultBytes < 0:
		return nil, fmt.Errorf("mcp: the body and result limits must not be negative.%s", hint)
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	if cfg.MaxResultBytes == 0 {
		cfg.MaxResultBytes = 256 << 10
	}
	o := options{logger: slog.Default(), meter: otel.GetMeterProvider(), tracer: otel.GetTracerProvider()}
	for _, opt := range opts {
		opt(&o)
	}
	s := &Server{cfg: cfg, logger: o.logger, tools: make(map[string]ToolDef)}
	s.sdk = sdkmcp.NewServer(&sdkmcp.Implementation{Name: cfg.Name, Version: cfg.Version, Title: cfg.Title}, &sdkmcp.ServerOptions{Instructions: cfg.Instructions, Logger: o.logger})
	s.tracer = o.tracer.Tracer(instrumentationName)
	duration, err := o.meter.Meter(instrumentationName).Float64Histogram("mcp.server.operation.duration", metric.WithUnit("s"), metric.WithDescription("MCP tool call duration."))
	if err != nil {
		return nil, fmt.Errorf("mcp: cannot create the duration histogram: %w.%s", err, hint)
	}
	s.duration = duration
	s.sdk.AddReceivingMiddleware(s.receiving)
	return s, nil
}

// SDK returns the underlying server for features outside the typed tool API.
func (s *Server) SDK() *sdkmcp.Server { return s.sdk }

// Tools returns independent tool definitions sorted by name.
func (s *Server) Tools() []ToolDef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tools := make([]ToolDef, 0, len(s.tools))
	for _, def := range s.tools {
		def.Roles = slices.Clone(def.Roles)
		tools = append(tools, def)
	}
	slices.SortFunc(tools, func(a, b ToolDef) int { return strings.Compare(a.Name, b.Name) })
	return tools
}
