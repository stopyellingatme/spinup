// Package mcp serves typed tools to AI clients over the Model Context Protocol.
// The server is stateless and returns JSON responses at [Path].
//
// Create a server with [New], register handlers with [Tool], and mount [Server.Routes].
// The app must authenticate API keys before the MCP handler runs.
// The API key middleware needs apikeys.WithRPCPath(Path) to accept read keys on POST.
// Cookie authentication alone does not permit access to the MCP endpoint.
//
// Read keys can list and call only read-only tools.
// A tool with roles also requires a user with one of those roles.
// [ToolError] returns an error message that is safe for the model.
// Other handler errors and panics return a fixed message and log the cause.
// Each tool call preserves the request deadline and sets the audit actor reason.
// The endpoint rejects duplicate JSON names and invalid UTF-8 before SDK dispatch.
// Schemas use the string type for custom types that encode as JSON strings.
//
// [Server.Command] prints tool definitions and inferred schemas without a database.
// [Server.SDK] provides access to SDK features outside the typed tool API.
// The package is under kit/x, so its API can change before v1.
package mcp
