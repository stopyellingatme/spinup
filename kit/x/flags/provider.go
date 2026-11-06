package flags

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// ProviderName is the name in Metadata and in the span event.
const ProviderName = "spinup"

// Metadata names a provider.
type Metadata struct{ Name string }

// Provider is the OpenFeature provider shape for booleans. An adapter can wrap it later.
type Provider interface {
	Metadata() Metadata
	BooleanEvaluation(ctx context.Context, key string, defaultValue bool, subject Subject) Resolution
}

// Flags is the provider of the kit. It holds the declarations and the
// current snapshot. A store replaces the snapshot; evaluations read it.
// A Flags is safe for concurrent use.
type Flags struct {
	declared Declared
	empty    *Snapshot
	snapshot atomic.Pointer[Snapshot]
	logger   *slog.Logger
}

var _ Provider = (*Flags)(nil)

// options are the settings of New.
type options struct {
	declared Declared
	logger   *slog.Logger
}

// Option configures New.
type Option func(*options)

// WithDeclared sets the declared flags, usually the generated Set.
// Without it, the set is empty and every key is undeclared.
func WithDeclared(d Declared) Option {
	return func(o *options) { o.declared = d }
}

// WithLogger sets the logger. The default is slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// New returns a provider for the declared flags. It validates the
// declarations (Declared.Validate). The error ends with a Try: line.
// The provider starts with an empty snapshot, so every declared flag
// answers its declared default until the first Replace.
func New(opts ...Option) (*Flags, error) {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	if err := o.declared.Validate(); err != nil {
		return nil, err
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	declared := o.declared.clone()
	return &Flags{
		declared: declared,
		empty:    NewSnapshot(declared, nil, time.Time{}),
		logger:   o.logger,
	}, nil
}

// Metadata returns {Name: "spinup"}.
func (f *Flags) Metadata() Metadata { return Metadata{Name: ProviderName} }

// Declared returns a copy of the declared flags.
func (f *Flags) Declared() Declared { return f.declared.clone() }

// Logger returns the logger of the provider. The store logs through it.
func (f *Flags) Logger() *slog.Logger { return f.logger }

// Snapshot returns the current snapshot, or an empty one with LoadedAt
// zero before the first Replace.
func (f *Flags) Snapshot() *Snapshot {
	if s := f.snapshot.Load(); s != nil {
		return s
	}
	return f.empty
}

// Replace swaps the current snapshot. The store calls it after a load.
// A scope that is already installed keeps its own snapshot. A nil s
// goes back to the empty snapshot.
func (f *Flags) Replace(s *Snapshot) { f.snapshot.Store(s) }

// Resolve evaluates key for s against the current snapshot. Jobs and
// commands use it when they have no request scope. It adds no span event.
func (f *Flags) Resolve(key string, s Subject) Resolution {
	return Resolve(f.Snapshot(), key, s)
}

// BooleanEvaluation is the OpenFeature evaluation. It returns
// defaultValue with ReasonError for an undeclared key. Otherwise it is
// Resolve. It adds the span event of ADR 0029 decision 12.
func (f *Flags) BooleanEvaluation(ctx context.Context, key string, defaultValue bool, s Subject) Resolution {
	r, ok := lookupOverride(key)
	if !ok {
		if _, declared := f.declared[key]; declared {
			r = resolve(f.Snapshot(), key, s)
		} else {
			r = resolution(defaultValue, ReasonError)
		}
	}
	addEvent(ctx, key, r)
	return r
}
