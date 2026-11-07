// Package flags evaluates the feature flags of a spinup app (ADR 0029).
//
// The package is under kit/x, so its API can change before v1. The owner
// declares flags in spinup.toml, spinup g flags writes the typed keys, and
// Postgres holds the runtime state. A request evaluates flags against one
// in-process snapshot.
//
// A flag is a boolean. Code asks Enabled(ctx, key) and gets one answer for
// the whole request. The answer comes from the scope in the context: the
// provider, one snapshot, and the subject. Middleware installs the scope
// after the auth middleware. A job or a command calls Flags.Context, or
// evaluates with Flags.Resolve. An evaluation runs no SQL and makes no
// allocation when no span records and no test override is active.
//
// Resolve applies the precedence of ADR 0029 decision 5. A user rule beats
// the global switch, the global switch beats a role rule, and a role rule
// beats the percentage. The percentage bucket of a user is sticky.
//
// Flags implements Provider, the OpenFeature provider shape for booleans.
// The package flagstest overrides flags in tests.
package flags
