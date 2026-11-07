package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
)

type testKeyContext struct{}

func installKeyReader(t *testing.T) {
	t.Helper()
	old := keyFrom
	keyFrom = func(ctx context.Context) (apikeys.Request, bool) {
		v, ok := ctx.Value(testKeyContext{}).(apikeys.Request)
		return v, ok
	}
	t.Cleanup(func() { keyFrom = old })
}
func requestContext(scope apikeys.Scope, role string) context.Context {
	ctx := auth.WithUser(context.Background(), auth.User{ID: 7, Email: "a@example.com", Role: role})
	if scope != "" {
		ctx = context.WithValue(ctx, testKeyContext{}, apikeys.Request{Key: apikeys.Key{ID: 42, Scope: scope}})
	}
	return ctx
}
func newServer(t *testing.T, cfg Config, opts ...Option) *Server {
	t.Helper()
	if cfg.Name == "" {
		cfg.Name = "app"
	}
	if cfg.Version == "" {
		cfg.Version = "v1"
	}
	opts = append([]Option{WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}, opts...)
	s, err := New(cfg, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func route(s *Server) http.Handler {
	mux := http.NewServeMux()
	s.Routes("https://app.example")(mux)
	return mux
}
func rpc(t *testing.T, h http.Handler, ctx context.Context, method, params string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, Path, strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":%q,"params":%s}`, method, params)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestRequestChecks(t *testing.T) {
	installKeyReader(t)
	h := route(newServer(t, Config{}))
	for _, tc := range []struct {
		name, origin string
		hasOrigin    bool
		scope        apikeys.Scope
		status       int
	}{
		{name: "anonymous", status: 401}, {name: "cookie only", status: 401},
		{name: "bad origin first", origin: "https://evil.example", hasOrigin: true, status: 403},
		{name: "empty origin", hasOrigin: true, status: 403},
		{name: "origin with path", origin: "https://app.example/path", hasOrigin: true, scope: apikeys.ScopeWrite, status: 403},
		{name: "case match", origin: "HTTPS://APP.EXAMPLE", hasOrigin: true, scope: apikeys.ScopeWrite, status: 405},
		{name: "no origin", scope: apikeys.ScopeRead, status: 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := requestContext(tc.scope, "staff")
			if tc.name == "anonymous" {
				ctx = context.Background()
			}
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, Path, nil)
			if tc.hasOrigin {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.status == 401 || tc.status == 403 {
				var p httpx.Problem
				if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
					t.Fatal(err)
				}
				expected := "The MCP endpoint needs an API key. Try: send Authorization: Bearer <key>."
				if tc.status == 403 {
					expected = "The Origin header is not allowed. Try: call the MCP endpoint from a server-side client, or use PUBLIC_ORIGIN."
				}
				if p.Detail != expected || p.Status != tc.status || w.Header().Get("Content-Type") != "application/problem+json" {
					t.Fatalf("problem: %+v", p)
				}
			}
			challenge := w.Header().Get("WWW-Authenticate")
			if tc.status == 401 && challenge != `Bearer resource_metadata="https://app.example/.well-known/oauth-protected-resource/api/mcp"` {
				t.Fatalf("challenge: %s", challenge)
			}
			if tc.status == 403 && challenge != "" {
				t.Fatalf("origin rejection included challenge: %s", challenge)
			}
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		r := httptest.NewRequestWithContext(requestContext(apikeys.ScopeWrite, "staff"), method, Path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 405 {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
		}
	}
}
func TestMetadata(t *testing.T) {
	for _, title := range []string{"", "My App"} {
		h := route(newServer(t, Config{Title: title}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, MetadataPath, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "public, max-age=3600" {
			t.Fatalf("metadata: %+v", w)
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		name := title
		if name == "" {
			name = "app"
		}
		want := map[string]any{"resource": "https://app.example/api/mcp", "resource_name": name, "bearer_methods_supported": []any{"header"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("metadata: %#v", got)
		}
	}
}
func TestPublicOriginTrailingSlash(t *testing.T) {
	installKeyReader(t)
	mux := http.NewServeMux()
	newServer(t, Config{}).Routes("https://app.example/")(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, MetadataPath, nil))
	if !strings.Contains(w.Body.String(), `"resource":"https://app.example/api/mcp"`) {
		t.Fatalf("metadata: %s", w.Body.String())
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, Path, strings.NewReader("{}"))
	r.Header.Set("Origin", "https://app.example")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code == http.StatusForbidden {
		t.Fatalf("the public origin was refused: %s", w.Body.String())
	}
	if got := w.Header().Get("WWW-Authenticate"); got != "" && strings.Contains(got, "example//") {
		t.Fatalf("WWW-Authenticate: %s", got)
	}
}

func registerTestTool(s *Server, def ToolDef) {
	Tool(s, def, func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
}
func TestToolAccess(t *testing.T) {
	installKeyReader(t)
	s := newServer(t, Config{})
	registerTestTool(s, ToolDef{Name: "write", Description: "Write", Destructive: true})
	registerTestTool(s, ToolDef{Name: "read", Description: "Read", ReadOnly: true, Idempotent: true})
	registerTestTool(s, ToolDef{Name: "admin", Description: "Admin", ReadOnly: true, Roles: []string{"manager"}})
	h := route(s)
	for _, tc := range []struct {
		scope apikeys.Scope
		role  string
		want  []string
	}{
		{apikeys.ScopeRead, "staff", []string{"read"}}, {apikeys.ScopeWrite, "staff", []string{"read", "write"}},
		{apikeys.ScopeRead, "manager", []string{"admin", "read"}}, {apikeys.ScopeWrite, "manager", []string{"admin", "read", "write"}},
		{apikeys.ScopeWrite, "", []string{"read", "write"}},
	} {
		ctx := requestContext(tc.scope, tc.role)
		w := rpc(t, h, ctx, "tools/list", `{}`)
		var body struct{ Result sdkmcp.ListToolsResult }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, tool := range body.Result.Tools {
			names = append(names, tool.Name)
			a := tool.Annotations
			if a == nil || a.OpenWorldHint == nil || *a.OpenWorldHint || a.ReadOnlyHint != (tool.Name != "write") {
				t.Fatalf("annotations: %+v", a)
			}
			if tool.Name == "write" {
				if a.DestructiveHint == nil || !*a.DestructiveHint {
					t.Fatalf("write annotations: %+v", a)
				}
			} else if a.DestructiveHint != nil {
				t.Fatalf("read annotations: %+v", a)
			}
		}
		if !reflect.DeepEqual(names, tc.want) || body.Result.CacheScope != "private" {
			t.Fatalf("%s %s: %s", tc.scope, tc.role, w.Body.String())
		}
	}
	for _, tc := range []struct {
		scope               apikeys.Scope
		role, tool, message string
	}{
		{apikeys.ScopeRead, "staff", "write", "The tool write is not available for this key. Try: use a key with the write scope."},
		{apikeys.ScopeWrite, "staff", "admin", "The tool admin is not available for your role. Try: use an account with a required role."},
	} {
		w := rpc(t, h, requestContext(tc.scope, tc.role), "tools/call", fmt.Sprintf(`{"name":%q,"arguments":{}}`, tc.tool))
		var body struct{ Error *jsonrpc.Error }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error == nil || body.Error.Code != jsonrpc.CodeInvalidParams || body.Error.Message != tc.message {
			t.Fatalf("call refusal: %s", w.Body.String())
		}
	}
	for _, tool := range []string{"read", "write", "admin"} {
		w := rpc(t, h, requestContext(apikeys.ScopeWrite, "manager"), "tools/call", fmt.Sprintf(`{"name":%q,"arguments":{}}`, tool))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"structuredContent":{}`) {
			t.Fatalf("call %s: %d %s", tool, w.Code, w.Body.String())
		}
	}
}

func TestOriginMatching(t *testing.T) {
	for _, tc := range []struct {
		origin  string
		allowed bool
	}{
		{"https://app.example", true}, {"HTTPS://APP.EXAMPLE", true},
		{"http://app.example", false}, {"https://app.example:443", false},
		{"https://app.example/", false}, {"https://app.example#", false}, {"https://app.example#fragment", false},
		{"https://app.example?", false}, {"https://app.example?query", false},
		{"https://user@app.example", false}, {"https://app.example.evil", false},
		{"null", false}, {"", false}, {"https://app.example https://evil.example", false},
	} {
		if got := sameOrigin(tc.origin, "https://app.example"); got != tc.allowed {
			t.Errorf("origin %q: %v", tc.origin, got)
		}
	}
}

func TestMultipleOrigins(t *testing.T) {
	installKeyReader(t)
	r := httptest.NewRequestWithContext(requestContext(apikeys.ScopeWrite, "staff"), http.MethodPost, Path, nil)
	r.Header["Origin"] = []string{"https://app.example", "https://evil.example"}
	w := httptest.NewRecorder()
	route(newServer(t, Config{})).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("multiple origins: %d %s", w.Code, w.Body.String())
	}
}
