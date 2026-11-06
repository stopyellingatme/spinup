// Package flags evaluates the feature flags of a spinup app (ADR 0029).
//
// The package is under kit/x, so its API can change before v1. The owner
// declares flags in spinup.toml, spinup g flags writes the typed keys, and
// Postgres holds the runtime state. A request evaluates flags against one
// in-process snapshot.
package flags
