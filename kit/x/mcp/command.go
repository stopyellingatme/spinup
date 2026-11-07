package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const commandUsage = "mcp tools"

// Command returns the operator command that prints the tools as JSON.
// Usage errors include the usage text for the app to print with exit status 2.
func (s *Server) Command() func(context.Context, []string) error {
	return func(ctx context.Context, args []string) error { return s.runCommand(ctx, args, os.Stdout) }
}

func commandError(why string) error {
	return fmt.Errorf("mcp: %s.\nTry: %s", why, commandUsage)
}

type commandTool struct {
	Name         string                  `json:"name"`
	Title        string                  `json:"title"`
	Description  string                  `json:"description"`
	Annotations  *sdkmcp.ToolAnnotations `json:"annotations"`
	Roles        []string                `json:"roles"`
	InputSchema  any                     `json:"inputSchema"`
	OutputSchema any                     `json:"outputSchema"`
}

func (s *Server) runCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError("no action was given")
	}
	if args[0] != "tools" {
		return commandError(fmt.Sprintf("unknown action %q", args[0]))
	}
	if len(args) != 1 {
		return commandError("tools accepts no additional arguments")
	}
	tools, err := s.commandTools(ctx)
	if err != nil {
		return commandError("cannot list tools: " + err.Error())
	}
	if err = json.NewEncoder(out).Encode(tools); err != nil {
		return commandError("cannot write JSON: " + err.Error())
	}
	return nil
}

func (s *Server) commandTools(ctx context.Context) ([]commandTool, error) {
	// Use the SDK's in-memory transport to obtain its inferred schemas.
	// The private context marker permits only this local list to include every tool.
	ctx = context.WithValue(ctx, commandListKey{}, true)
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	server, err := s.sdk.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = server.Close() }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: s.cfg.Name + "-tools", Version: s.cfg.Version}, &sdkmcp.ClientOptions{Logger: s.logger})
	session, err := client.Connect(ctx, clientTransport, &sdkmcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()
	definitions := make(map[string]ToolDef)
	for _, def := range s.Tools() {
		definitions[def.Name] = def
	}
	tools := make([]commandTool, 0, len(definitions))
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		roles := append([]string{}, definitions[tool.Name].Roles...)
		tools = append(tools, commandTool{Name: tool.Name, Title: tool.Title, Description: tool.Description, Annotations: tool.Annotations, Roles: roles, InputSchema: tool.InputSchema, OutputSchema: tool.OutputSchema})
	}
	slices.SortFunc(tools, func(a, b commandTool) int { return strings.Compare(a.Name, b.Name) })
	return tools, nil
}
