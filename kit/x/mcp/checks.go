package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
)

var keyFrom = apikeys.KeyFrom

type commandListKey struct{}

// Routes registers the MCP endpoint and the public metadata endpoint.
func (s *Server) Routes(publicOrigin string) func(*http.ServeMux) {
	// PUBLIC_ORIGIN can end in a slash. An origin never does.
	publicOrigin = strings.TrimRight(publicOrigin, "/")
	sdkHandler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return s.sdk }, &sdkmcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: s.cfg.MaxBodyBytes, PropagateRequestCancellation: true,
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins := r.Header.Values("Origin")
		if len(origins) > 0 && (len(origins) != 1 || !sameOrigin(origins[0], publicOrigin)) {
			httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusForbidden, Detail: "The Origin header is not allowed. Try: call the MCP endpoint from a server-side client, or use PUBLIC_ORIGIN."})
			return
		}
		if _, ok := keyFrom(r.Context()); !ok {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+publicOrigin+MetadataPath+`"`)
			httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnauthorized, Detail: "The MCP endpoint needs an API key. Try: send Authorization: Bearer <key>."})
			return
		}
		if r.Body != nil {
			body := &observedBody{ReadCloser: r.Body, limit: s.cfg.MaxBodyBytes}
			r = r.WithContext(context.WithValue(r.Context(), bodyKey{}, body))
			r.Body = body
		}
		sdkHandler.ServeHTTP(w, r)
	})
	return func(mux *http.ServeMux) {
		mux.Handle(Path, handler)
		mux.HandleFunc("GET "+MetadataPath, s.metadata(publicOrigin))
	}
}

func sameOrigin(origin, publicOrigin string) bool {
	normalize := func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.Path != "" {
			return ""
		}
		u.Scheme = strings.ToLower(u.Scheme)
		if u.Scheme != "http" && u.Scheme != "https" {
			return ""
		}
		u.Host = strings.ToLower(u.Host)
		return u.String()
	}
	origin = normalize(origin)
	return origin != "" && origin == normalize(publicOrigin)
}

func (s *Server) refusal(ctx context.Context, name string) error {
	s.mu.RLock()
	def, known := s.tools[name]
	s.mu.RUnlock()
	key, ok := keyFrom(ctx)
	scopeAllowed := key.Key.Scope == apikeys.ScopeWrite || (key.Key.Scope == apikeys.ScopeRead && def.ReadOnly)
	if !known || !ok || !scopeAllowed {
		return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("The tool %s is not available for this key. Try: use a key with the write scope.", name)}
	}
	if len(def.Roles) > 0 {
		user, _ := auth.UserFrom(ctx)
		if !user.HasRole(def.Roles...) {
			return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("The tool %s is not available for your role. Try: use an account with a required role.", name)}
		}
	}
	return nil
}

func (s *Server) receiving(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
	return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
		if method == "tools/call" {
			return s.call(ctx, req, next)
		}
		res, err := next(ctx, method, req)
		if method == "tools/list" && err == nil {
			if list, ok := res.(*sdkmcp.ListToolsResult); ok && list != nil {
				// The command lists every tool. HTTP callers always use the caller filter.
				operator, _ := ctx.Value(commandListKey{}).(bool)
				tools := make([]*sdkmcp.Tool, 0, len(list.Tools))
				for _, tool := range list.Tools {
					if operator || s.refusal(ctx, tool.Name) == nil {
						tools = append(tools, tool)
					}
				}
				slices.SortFunc(tools, func(a, b *sdkmcp.Tool) int { return strings.Compare(a.Name, b.Name) })
				list.Tools = tools
				list.CacheScope = "private"
			}
		}
		return res, err
	}
}
