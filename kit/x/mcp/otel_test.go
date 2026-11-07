package mcp

import (
	"context"
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestTelemetry(t *testing.T) {
	installKeyReader(t)
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()); _ = mp.Shutdown(context.Background()) })
	s := newServer(t, Config{}, WithTracerProvider(tp), WithMeterProvider(mp))
	for _, name := range []string{"success", "safe", "failure", "panic"} {
		Tool(s, ToolDef{Name: name, Description: "Test"}, func(context.Context, struct{}) (testOutput, error) {
			switch name {
			case "safe":
				return testOutput{}, ToolError("Fix input. Try: retry.")
			case "failure":
				return testOutput{}, errors.New("secret failure")
			case "panic":
				panic("secret panic")
			default:
				return testOutput{"secret output"}, nil
			}
		})
	}
	ctx, parent := tp.Tracer("test").Start(requestContext(apikeys.ScopeWrite, "staff"), "http")
	for _, name := range []string{"success", "safe", "failure", "panic"} {
		w := rpc(t, route(s), ctx, "tools/call", `{"name":"`+name+`","arguments":{}}`)
		if w.Code != 200 {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
	// Rejected calls must also get a span and a duration measurement.
	_ = rpc(t, route(s), requestContext(apikeys.ScopeRead, "staff"), "tools/call", `{"name":"success","arguments":{}}`)
	parent.End()
	spans := exporter.GetSpans()
	counts := map[string]int{}
	for _, span := range spans {
		if span.Name == "http" {
			continue
		}
		counts[span.Name]++
		attrs := map[string]attribute.Value{}
		for _, kv := range span.Attributes {
			attrs[string(kv.Key)] = kv.Value
		}
		name := attrs["gen_ai.tool.name"].AsString()
		if span.Name != "tools/call "+name || span.SpanKind != trace.SpanKindServer || attrs["mcp.method.name"].AsString() != "tools/call" || attrs["gen_ai.operation.name"].AsString() != "execute_tool" || attrs["mcp.protocol.version"].AsString() != "2025-11-25" || attrs["jsonrpc.request.id"].AsString() != "9" {
			t.Fatalf("span: %+v", span)
		}
		failure := attrs["error.type"].AsString()
		if failure == "" {
			if name != "success" || len(attrs) != 5 || span.Parent.SpanID() != parent.SpanContext().SpanID() {
				t.Fatalf("success span: %+v", span)
			}
		} else if len(attrs) != 6 || span.Status.Code != codes.Error {
			t.Fatalf("failure span: %+v", span)
		}
	}
	if len(spans) != 6 || counts["tools/call success"] != 2 || counts["tools/call safe"] != 1 || counts["tools/call failure"] != 1 || counts["tools/call panic"] != 1 {
		t.Fatalf("spans: %+v", spans)
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	count := uint64(0)
	failures := uint64(0)
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "mcp.server.operation.duration" || m.Unit != "s" {
				t.Fatalf("metric: %+v", m)
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric data: %T", m.Data)
			}
			for _, point := range hist.DataPoints {
				count += point.Count
				attrs := point.Attributes
				method, _ := attrs.Value(attribute.Key("mcp.method.name"))
				name, _ := attrs.Value(attribute.Key("gen_ai.tool.name"))
				if method.AsString() != "tools/call" || name.AsString() == "" || point.Sum <= 0 {
					t.Fatalf("point: %+v", point)
				}
				kind, failed := attrs.Value(attribute.Key("error.type"))
				if failed {
					failures += point.Count
					if kind.AsString() == "" {
						t.Fatal("empty metric error.type")
					}
				}
				if name.AsString() != "success" && !failed {
					t.Fatalf("missing metric error.type: %+v", attrs)
				}
				if attrs.Len() != 2 && (!failed || attrs.Len() != 3) {
					t.Fatalf("metric attributes: %+v", attrs)
				}
			}
		}
	}
	if count != 5 || failures != 4 {
		t.Fatalf("histogram count: %d, failures: %d", count, failures)
	}
}

func TestProtocolAndRequestID(t *testing.T) {
	installKeyReader(t)
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	s := newServer(t, Config{}, WithTracerProvider(tp))
	registerTestTool(s, ToolDef{Name: "read", Description: "Read", ReadOnly: true})
	// Test the request metadata path without an HTTP transport.
	req := &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{Name: "read", Meta: sdkmcp.Meta{sdkmcp.MetaKeyProtocolVersion: "2026-07-28"}}}
	ctx := context.WithValue(requestContext(apikeys.ScopeRead, "staff"), requestIDsKey{}, readRequestIDs([]byte(`{"id":"call-id"}`)))
	_, err := s.call(ctx, req, func(context.Context, string, sdkmcp.Request) (sdkmcp.Result, error) {
		return &sdkmcp.CallToolResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatal(spans)
	}
	attrs := attribute.NewSet(spans[0].Attributes...)
	version, _ := attrs.Value("mcp.protocol.version")
	id, _ := attrs.Value("jsonrpc.request.id")
	if version.AsString() != "2026-07-28" || id.AsString() != "call-id" {
		t.Fatalf("attributes: %+v", attrs)
	}
}
