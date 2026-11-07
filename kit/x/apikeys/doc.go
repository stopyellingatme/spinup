// Package apikeys gives each user of a spinup app API keys with plan quotas
// (ADR 0030).
//
// The package is under kit/x, so its API can change before v1. The owner
// declares the plans in the [apikeys] table of spinup.toml, spinup g apikeys
// writes them to api/internal/apikeys/apikeys.gen.go, and Postgres holds the
// keys, the plan of each user, and a usage counter for each key and UTC day.
//
// A key is a Bearer token. Store implements auth.Bearer, so the kit auth
// middleware puts the user of the key in the request context. The Store
// middleware then applies the scope of the key (read or write) and the plan
// quota: a limit for each minute in memory, and a limit for each day that the
// replicas share through Postgres. All keys of a user share the plan budget.
package apikeys
