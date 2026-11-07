package mcp

import (
	"context"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/stopyellingatme/spinup/kit/x/mcp"

type callStateKey struct{}
type callState struct{ errorType string }

func setCallError(ctx context.Context, kind string) {
	if state, ok := ctx.Value(callStateKey{}).(*callState); ok {
		state.errorType = kind
	}
}

func (s *Server) observe(ctx context.Context, req sdkmcp.Request, name string) (context.Context, func()) {
	state := &callState{}
	ctx = context.WithValue(ctx, callStateKey{}, state)
	protocol := ""
	if versioned, ok := req.(interface{ ProtocolVersion() string }); ok {
		protocol = versioned.ProtocolVersion()
	}
	if protocol == "" && req.GetExtra() != nil {
		protocol = req.GetExtra().Header.Get("MCP-Protocol-Version")
	}
	id := ""
	if ids, ok := ctx.Value(requestIDsKey{}).(*requestIDs); ok {
		id = ids.take(req)
	}
	attrs := []attribute.KeyValue{attribute.String("mcp.method.name", "tools/call"), attribute.String("gen_ai.tool.name", name)}
	ctx, span := s.tracer.Start(ctx, "tools/call "+name, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(
		attrs[0], attrs[1], attribute.String("gen_ai.operation.name", "execute_tool"), attribute.String("mcp.protocol.version", protocol), attribute.String("jsonrpc.request.id", id),
	))
	start := time.Now()
	return ctx, func() {
		if state.errorType != "" {
			failure := attribute.String("error.type", state.errorType)
			attrs = append(attrs, failure)
			span.SetAttributes(failure)
			span.SetStatus(codes.Error, state.errorType)
		}
		if s.duration != nil {
			s.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
		}
		span.End()
	}
}
