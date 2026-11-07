package mcp

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stopyellingatme/spinup/kit/audit"
)

// ToolDef describes a typed tool and the callers that can use it.
type ToolDef struct {
	// Name is a unique name that matches [a-z][a-z0-9_]{0,63}.
	Name string
	// Title is an optional display name.
	Title string
	// Description explains the tool to the model. It is required.
	Description string
	// ReadOnly permits read keys to use this tool.
	ReadOnly bool
	// Destructive marks a write tool that can remove data.
	Destructive bool
	// Idempotent marks a tool whose repeated calls have no additional effect.
	Idempotent bool
	// Roles restricts the tool to users with one of these roles.
	Roles []string
}

var toolName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

const failedText = "The tool failed. Try: retry the call, or report the problem."

type toolError struct{ message string }

func (e *toolError) Error() string { return e.message }

// ToolError makes an error whose message is safe for the model.
func ToolError(format string, args ...any) error {
	return &toolError{message: fmt.Sprintf(format, args...)}
}

// Tool registers a typed handler. An invalid or duplicate definition causes a panic.
func Tool[In, Out any](s *Server, def ToolDef, h func(context.Context, In) (Out, error)) {
	const hint = "\nTry: correct the definition passed to mcp.Tool."
	switch {
	case !toolName.MatchString(def.Name):
		panic("mcp: the tool name must match [a-z][a-z0-9_]{0,63}." + hint)
	case strings.TrimSpace(def.Description) == "":
		panic("mcp: the tool description is empty." + hint)
	case def.ReadOnly && def.Destructive:
		panic("mcp: a read-only tool cannot be destructive." + hint)
	}
	def.Roles = slices.Clone(def.Roles)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tools[def.Name]; exists {
		panic("mcp: the tool name " + def.Name + " is already registered." + hint)
	}
	sdkmcp.AddTool(s.sdk, &sdkmcp.Tool{Name: def.Name, Title: def.Title, Description: def.Description, Annotations: annotations(def)},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in In) (*sdkmcp.CallToolResult, Out, error) {
			actor, _ := audit.ActorFrom(ctx)
			key, _ := keyFrom(ctx)
			actor.Reason = fmt.Sprintf("api_key:%d mcp:%s", key.Key.ID, def.Name)
			ctx = audit.WithActor(ctx, actor)
			out, err := h(ctx, in)
			if err != nil {
				var safe *toolError
				if errors.As(err, &safe) {
					setCallError(ctx, "tool_error")
					err = errors.New(safe.message)
				} else {
					setCallError(ctx, "internal_error")
					s.logger.ErrorContext(ctx, "MCP tool failed", "tool", def.Name, "error", err)
					err = &toolError{message: failedText}
				}
			}
			return nil, out, err
		})
	s.tools[def.Name] = def
}

func annotations(def ToolDef) *sdkmcp.ToolAnnotations {
	closed := false
	a := &sdkmcp.ToolAnnotations{ReadOnlyHint: def.ReadOnly, IdempotentHint: def.Idempotent, OpenWorldHint: &closed}
	if !def.ReadOnly {
		destructive := def.Destructive
		a.DestructiveHint = &destructive
	}
	return a
}
