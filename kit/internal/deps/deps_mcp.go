//go:build deps

package deps

// MCP module pre-seed (ADR 0031).
import (
	_ "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
)
