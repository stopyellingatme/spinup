package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
)

type testOutput struct {
	Value string `json:"value"`
}

func callResult(t *testing.T, s *Server, name string) (*sdkmcp.CallToolResult, string) {
	t.Helper()
	w := rpc(t, route(s), requestContext(apikeys.ScopeWrite, "staff"), "tools/call", fmt.Sprintf(`{"name":%q,"arguments":{}}`, name))
	var body struct{ Result *sdkmcp.CallToolResult }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || body.Result == nil {
		t.Fatalf("tool call: %d %s", w.Code, w.Body.String())
	}
	text := ""
	if len(body.Result.Content) > 0 {
		text = body.Result.Content[0].(*sdkmcp.TextContent).Text
	}
	return body.Result, text
}
func TestToolErrors(t *testing.T) {
	installKeyReader(t)
	for _, tc := range []struct {
		name      string
		handler   func(context.Context, struct{}) (testOutput, error)
		want, log string
	}{
		{"success", func(context.Context, struct{}) (testOutput, error) { return testOutput{"ok"}, nil }, `{"value":"ok"}`, ""},
		{"safe", func(context.Context, struct{}) (testOutput, error) {
			return testOutput{}, fmt.Errorf("wrapped: %w", ToolError("Bad %s. Try: fix it.", "input"))
		}, "Bad input. Try: fix it.", ""},
		{"error", func(context.Context, struct{}) (testOutput, error) {
			return testOutput{}, errors.New("secret database detail")
		}, failedText, "secret database detail"},
		{"panic", func(context.Context, struct{}) (testOutput, error) { panic("secret panic detail") }, failedText, "secret panic detail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			s := newServer(t, Config{}, WithLogger(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError}))))
			Tool(s, ToolDef{Name: tc.name, Description: "Test", ReadOnly: true}, tc.handler)
			res, text := callResult(t, s, tc.name)
			if text != tc.want || res.IsError != (tc.name != "success") {
				t.Fatalf("result: %+v %q", res, text)
			}
			if tc.log != "" && (!strings.Contains(logs.String(), tc.log) || !strings.Contains(logs.String(), `"level":"ERROR"`)) {
				t.Fatalf("logs: %s", logs.String())
			}
			if tc.name == "panic" && (!strings.Contains(logs.String(), `"stack":`) || !strings.Contains(logs.String(), "runtime/debug.Stack")) {
				t.Fatalf("missing panic stack: %s", logs.String())
			}
			if tc.log == "" && logs.Len() != 0 {
				t.Fatalf("unexpected logs: %s", logs.String())
			}
			if res.IsError && res.StructuredContent != nil {
				t.Fatalf("error included output: %+v", res)
			}
		})
	}
}
func TestResultCap(t *testing.T) {
	installKeyReader(t)
	s := newServer(t, Config{MaxResultBytes: 100})
	Tool(s, ToolDef{Name: "large", Description: "Large"}, func(context.Context, struct{}) (testOutput, error) { return testOutput{strings.Repeat("a", 100)}, nil })
	// The cap applies to the complete MCP result, including the JSON text copy.
	out, _ := json.Marshal(testOutput{strings.Repeat("a", 100)})
	original := &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(out)}}, StructuredContent: json.RawMessage(out)}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	res, text := callResult(t, s, "large")
	want := fmt.Sprintf("The result is too large (%d bytes, limit 100). Try: ask for a smaller page with limit.", len(data))
	if !res.IsError || text != want || res.StructuredContent != nil {
		t.Fatalf("cap: %+v %q, want %q", res, text, want)
	}
	// A result exactly at the cap must pass.
	s = newServer(t, Config{MaxResultBytes: int64(len(data))})
	Tool(s, ToolDef{Name: "large", Description: "Large"}, func(context.Context, struct{}) (testOutput, error) { return testOutput{strings.Repeat("a", 100)}, nil })
	res, _ = callResult(t, s, "large")
	if res.IsError {
		t.Fatalf("exact cap: %+v", res)
	}
}
func TestAuditReason(t *testing.T) {
	installKeyReader(t)
	s := newServer(t, Config{})
	var got audit.Actor
	Tool(s, ToolDef{Name: "write", Description: "Write"}, func(ctx context.Context, _ struct{}) (struct{}, error) {
		got, _ = audit.ActorFrom(ctx)
		if user, ok := auth.UserFrom(ctx); !ok || user.ID != 7 {
			t.Error("lost user context")
		}
		if key, ok := keyFrom(ctx); !ok || key.Key.ID != 42 {
			t.Error("lost key context")
		}
		return struct{}{}, nil
	})
	original := audit.Actor{Kind: audit.KindUser, ID: 7, Email: "a@example.com", Role: "staff", Source: audit.SourceAPI, Reason: "original", RequestID: "request-1"}
	ctx := audit.WithActor(requestContext(apikeys.ScopeWrite, "staff"), original)
	w := rpc(t, route(s), ctx, "tools/call", `{"name":"write","arguments":{}}`)
	want := original
	want.Reason = "api_key:42 mcp:write"
	if got != want {
		t.Fatalf("actor: %+v, want %+v (%s)", got, want, w.Body.String())
	}
	if unchanged, _ := audit.ActorFrom(ctx); unchanged != original {
		t.Fatalf("caller actor changed: %+v", unchanged)
	}
}
func TestBodyLimitAndInitialize(t *testing.T) {
	installKeyReader(t)
	s := newServer(t, Config{MaxBodyBytes: 500})
	w := rpc(t, route(s), requestContext(apikeys.ScopeRead, "staff"), "initialize", `{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"v1"}}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"app"`) || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("initialize: %d %s", w.Code, w.Body.String())
	}
	w = rpc(t, route(s), requestContext(apikeys.ScopeWrite, "staff"), "tools/list", `{"junk":"`+strings.Repeat("a", 501)+`"}`)
	if w.Code != 413 {
		t.Fatalf("body cap: %d %s", w.Code, w.Body.String())
	}
}

// brokenOutput tests failures that occur after the typed handler returns.
type brokenOutput struct {
	Value string `json:"value"`
}

func (o brokenOutput) MarshalJSON() ([]byte, error) {
	if o.Value == "panic" {
		panic("secret encoding panic")
	}
	return nil, errors.New("secret encoding error")
}
func TestOutputEncodingFailure(t *testing.T) {
	installKeyReader(t)
	for _, value := range []string{"error", "panic"} {
		t.Run(value, func(t *testing.T) {
			var logs bytes.Buffer
			s := newServer(t, Config{}, WithLogger(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError}))))
			Tool(s, ToolDef{Name: "broken", Description: "Broken"}, func(context.Context, struct{}) (brokenOutput, error) { return brokenOutput{value}, nil })
			res, text := callResult(t, s, "broken")
			if !res.IsError || text != failedText || !strings.Contains(logs.String(), "secret encoding "+value) {
				t.Fatalf("result=%+v text=%q logs=%s", res, text, logs.String())
			}
		})
	}
}
