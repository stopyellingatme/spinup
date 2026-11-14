// Package gen contains generated API types and server interfaces.
// Run spinup g openapi from the project root to bundle OpenAPI files first.
package gen

//go:generate go tool -modfile=../../tools.mod oapi-codegen -config ../../oapi-codegen.yaml ../../openapi/bundle.gen.yaml
//go:generate go tool -modfile=../../tools.mod sqlc generate -f ../../sqlc.yaml
