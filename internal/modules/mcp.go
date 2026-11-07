package modules

// MCPNextSteps returns the manual connection steps for an existing app.
func MCPNextSteps() string {
	return `Next: create a key for the MCP client:
 spinup apikeys create <email> --name <client> --scope read
Next: connect Claude Code:
 claude mcp add --transport http <app> <url>/api/mcp --header "Authorization: Bearer <key>"
Next: add a typed tool:
 spinup g mcp tool <name>
Next: use a write key for write tools. A read key can call only read-only tools.
`
}
