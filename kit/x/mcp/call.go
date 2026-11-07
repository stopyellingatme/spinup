package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func errorResult(text string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}}}
}

func (s *Server) call(ctx context.Context, req sdkmcp.Request, next sdkmcp.MethodHandler) (res sdkmcp.Result, err error) {
	ctx, release := toolContext(ctx)
	defer release()
	name := ""
	if params, ok := req.GetParams().(*sdkmcp.CallToolParamsRaw); ok && params != nil {
		name = params.Name
	}
	ctx, finish := s.observe(ctx, req, name)
	defer finish()
	defer func() {
		if p := recover(); p != nil {
			s.logger.ErrorContext(ctx, "MCP tool panicked", "tool", name, "panic", p, "stack", string(debug.Stack()))
			setCallError(ctx, "panic")
			res, err = errorResult(failedText), nil
		}
	}()
	if err = s.refusal(ctx, name); err != nil {
		setCallError(ctx, "invalid_params")
		return nil, err
	}
	res, err = next(ctx, "tools/call", req)
	if err != nil {
		var wire *jsonrpc.Error
		if errors.As(err, &wire) {
			setCallError(ctx, "invalid_params")
			return nil, err
		}
		s.logger.ErrorContext(ctx, "MCP tool failed", "tool", name, "error", err)
		setCallError(ctx, "internal_error")
		return errorResult(failedText), nil
	}
	result, ok := res.(*sdkmcp.CallToolResult)
	if !ok || result == nil {
		return res, nil
	}
	if result.IsError {
		if state, _ := ctx.Value(callStateKey{}).(*callState); state.errorType == "" {
			state.errorType = "tool_error"
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		s.logger.ErrorContext(ctx, "MCP result encoding failed", "tool", name, "error", err)
		setCallError(ctx, "internal_error")
		return errorResult(failedText), nil
	}
	if int64(len(data)) > s.cfg.MaxResultBytes {
		setCallError(ctx, "result_too_large")
		return errorResult(fmt.Sprintf("The result is too large (%d bytes, limit %d). Try: ask for a smaller page with limit.", len(data), s.cfg.MaxResultBytes)), nil
	}
	return result, nil
}

// toolContext preserves SDK values and cancellation, and restores the HTTP deadline.
// Older protocols suppress transport cancellation in the SDK.
func toolContext(ctx context.Context) (context.Context, func()) {
	request, ok := ctx.Value(requestContextKey{}).(context.Context)
	if !ok {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(request, func() { cancel(context.Cause(request)) })
	if err := request.Err(); err != nil {
		cancel(context.Cause(request))
	}
	deadlineCancel := func() {}
	if deadline, ok := request.Deadline(); ok {
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline)
	}
	return ctx, func() {
		stop()
		deadlineCancel()
		cancel(nil)
	}
}
