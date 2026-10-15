// Package resource generates a CRUD API resource (`spinup g resource`).
//
// A resource is a migration, sqlc queries, OpenAPI paths and schemas, the
// strict-server handlers with conversions between the store and API types,
// and a handler test. The first resource also writes the shared
// api/internal/handlers/server.go and switches api/internal/routes/routes_gen.go
// to the generated OpenAPI router. Other packages add files to each resource
// with RegisterPart.
package resource
