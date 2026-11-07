// Package mcp serves typed tools of a spinup app to AI clients over the Model
// Context Protocol (ADR 0031).
//
// The package is under kit/x, so its API can change before v1. The server is
// stateless and answers JSON on POST /api/mcp. A client authenticates with an
// API key of the apikeys module.
package mcp
