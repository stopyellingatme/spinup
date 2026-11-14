// Stub of unit LV3: the unit replaces this file.

package live

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Hub holds the streams of one process.
type Hub struct{}

// Option changes a setting of a Hub.
type Option func(*options)

type options struct{}

// New validates the options and the pool. It does no I/O.
func New(_ *pgxpool.Pool, _ ...Option) (*Hub, error) { panic("live: stub of unit LV3") }

// Start opens the LISTEN connection and starts the loops. ctx ends the loops.
func (h *Hub) Start(_ context.Context) error { panic("live: stub of unit LV3") }

// Ready returns an error before Start and after Drain begins.
func (h *Hub) Ready(_ context.Context) error { panic("live: stub of unit LV3") }

// Close stops the loops, closes the LISTEN connection, and waits for the workers.
func (h *Hub) Close(_ context.Context) error { panic("live: stub of unit LV3") }

// WithLogger sets the logger. The default is slog.Default().
func WithLogger(_ *slog.Logger) Option { return func(*options) {} }

// WithMeterProvider sets the meter provider. The default is otel.GetMeterProvider().
func WithMeterProvider(_ metric.MeterProvider) Option { return func(*options) {} }

// WithTracerProvider sets the tracer provider. The default is otel.GetTracerProvider().
func WithTracerProvider(_ trace.TracerProvider) Option { return func(*options) {} }

// WithMaxStreams sets the most streams of the process. The default is 2000.
func WithMaxStreams(_ int) Option { return func(*options) {} }

// WithMaxStreamsPerUser sets the most streams of one user. The default is 8.
func WithMaxStreamsPerUser(_ int) Option { return func(*options) {} }

// WithMaxStreamAge sets the longest age of a stream. The default is 15 minutes.
func WithMaxStreamAge(_ time.Duration) Option { return func(*options) {} }

// WithHeartbeat sets the time between two pings of an idle stream. The default is 15 seconds.
func WithHeartbeat(_ time.Duration) Option { return func(*options) {} }

// WithPollInterval sets the time between two polls. The default is 5 seconds.
func WithPollInterval(_ time.Duration) Option { return func(*options) {} }

// WithRing sets the most patches of one ring and the most bytes of all rings.
// The defaults are 1024 and 8 MiB.
func WithRing(_ int, _ int64) Option { return func(*options) {} }

// WithBootstrapLimit sets the number of bootstrap slots. The default is 4.
func WithBootstrapLimit(_ int) Option { return func(*options) {} }

// WithBootstrapWait sets the longest wait for a bootstrap slot. The default is 2 seconds.
func WithBootstrapWait(_ time.Duration) Option { return func(*options) {} }

// WithRetention sets the patch age that the hub prunes. The default is 1 hour.
func WithRetention(_ time.Duration) Option { return func(*options) {} }

// WithIdleTTL sets how long the hub keeps a feed with no stream. The default is 60 seconds.
func WithIdleTTL(_ time.Duration) Option { return func(*options) {} }

// WithWriteTimeout sets the write timeout of a stream. The default is 10 seconds.
func WithWriteTimeout(_ time.Duration) Option { return func(*options) {} }

// WithSnapshotLimit sets the most rows of a snapshot. The default is 500.
func WithSnapshotLimit(_ int) Option { return func(*options) {} }
