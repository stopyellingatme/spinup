package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	if body, ok := ctx.Value(bodyKey{}).(*observedBody); ok {
		id = body.requestID()
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

type bodyKey struct{}

// observedBody captures at most the body cap. The SDK reads the body before dispatch.
// Only the JSON-RPC ID is used. Arguments never become telemetry attributes.
type observedBody struct {
	io.ReadCloser
	raw   []byte
	limit int64
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	keep := int64(n)
	if remaining := b.limit - int64(len(b.raw)); keep > remaining {
		keep = remaining
	}
	if keep > 0 {
		b.raw = append(b.raw, p[:keep]...)
	}
	return n, err
}
func (b *observedBody) requestID() string {
	var envelope struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(b.raw, &envelope) != nil {
		return ""
	}
	raw := bytes.TrimSpace(envelope.ID)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var id string
		if json.Unmarshal(raw, &id) == nil {
			return id
		}
		return ""
	}
	return string(raw)
}
