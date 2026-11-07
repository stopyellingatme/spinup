# ADR 0031: MCP server module

Status: accepted (owner, 2026-11-06)
Date: 2026-11-06
Extends: ADR 0013 section 1 (modules), ADR 0024 (roles), ADR 0025 (audit trail), ADR 0029 (the module pattern), ADR 0030 (API keys)

## Context

A generated app with the `apikeys` module has a keyed REST API.
AI clients (Claude, Cursor, and other agents) use the Model Context Protocol (MCP), not REST.
An app owner must write the MCP server by hand today.

Research on 2026-11-06 found these facts:

- The current MCP spec is 2026-07-28. It removes sessions, `Mcp-Session-Id`, the GET stream, and resumability. Each request is one POST.
- The official Go SDK `github.com/modelcontextprotocol/go-sdk` v1.8.0 accepts 2026-07-28 only with `Stateless: true`. Older clients fall back to 2025-11-25.
- The spec says: if the `Origin` header is present and invalid, the server must answer 403. The SDK turned its own cross-origin check off in v1.6.0.
- Authorization is optional in the spec. A server that supports it must serve RFC 9728 metadata and answer 401 with `WWW-Authenticate: Bearer resource_metadata="..."`.
- Claude ("No sign-in" with an `Authorization` header) and Cursor (`headers` in `mcp.json`) accept a static Bearer key. ChatGPT accepts only OAuth 2.1.
- The apikeys middleware rejects a read key on every method except GET and HEAD. MCP uses POST only.
- Caddy 2.11.6 and later flush `text/event-stream` immediately, also behind `encode`. The spinup pin is 2.11.7.

The owner decided these points on 2026-11-06. They are binding:

1. The feature is an opt-in module `mcp`. It requires `auth` and `apikeys`.
2. An MCP client authenticates with an API key. An OAuth 2.1 authorization server is a later ADR. ChatGPT is out of scope until then.
3. Tools are typed Go functions in scaffold files. The generators `spinup g mcp tool <name>` and `spinup g resource <Name> --mcp` write them.
4. The kit uses the official go-sdk v1.8.0.

## Decisions

### 1. Promise and limits

1.1. The endpoint is `POST /api/mcp`. It is under `/api/`, so Caddy sends it to the Go API in every frontend mode.
1.2. The server is stateless. It sends JSON responses, not SSE streams. Any replica serves any request. Blue/green deploys need no sticky sessions.
1.3. GET and DELETE on `/api/mcp` answer 405 (the SDK does this in stateless mode).
1.4. A tool call obeys `REQUEST_TIMEOUT` (default 30 s), the same as a REST request. The MCP route is not exempt from the deadline. Long work goes to `kit/jobs`, and the tool returns a handle.
1.5. One HTTP request is one plan request of the apikeys quota.
1.6. Out of scope: OAuth, `subscriptions/listen`, prompts and resources helpers (`SDK()` is the escape hatch), and SSE progress.
1.7. The SDK decodes tool arguments into `map[string]any`, so a JSON number becomes a `float64`. An integer above 2^53 loses precision. A `bigserial` ID never gets there in practice. A tool that needs exact large integers must take them as strings.

### 2. Dependencies

2.1. `kit/go.mod` requires `github.com/modelcontextprotocol/go-sdk v1.8.0`. The kit imports only the packages `mcp` and `jsonrpc`. It does not import the SDK `auth` package, so `golang-jwt` stays out of the build.
2.2. The new indirect modules are `github.com/google/jsonschema-go v0.4.3`, `github.com/segmentio/encoding v0.5.4`, `github.com/segmentio/asm v1.2.1`, `github.com/yosida95/uritemplate/v3 v3.0.2`, and `golang.org/x/time v0.15.0`.
2.3. The api pack `go.mod.tmpl` lists go-sdk as `// indirect`. Generated code imports only `kit/x/mcp`. `kit/x/mcp` must not put an SDK type in a signature that a generated file must name, except `SDK()`.
2.4. `internal/stackversions/stackversions.yaml` pins go-sdk under `go_modules`.

### 3. Kit API: package `kit/x/mcp`

```go
package mcp // import "github.com/stopyellingatme/spinup/kit/x/mcp"

const Path = "/api/mcp"
const MetadataPath = "/.well-known/oauth-protected-resource" + Path

type Config struct {
	Name         string // implementation name; required
	Version      string // implementation version; required
	Title        string // optional display name
	Instructions string // optional server instructions for the model
	MaxBodyBytes   int64 // request body cap; 0 means 1 MiB
	MaxResultBytes int64 // JSON size cap of one tool result; 0 means 256 KiB
}

type Option func(*options)
func WithLogger(l *slog.Logger) Option        // default slog.Default()
func WithMeterProvider(mp metric.MeterProvider) Option // default otel.GetMeterProvider()
func WithTracerProvider(tp trace.TracerProvider) Option // default otel.GetTracerProvider()

func New(cfg Config, opts ...Option) (*Server, error) // validates cfg; no I/O

type ToolDef struct {
	Name        string   // required; snake_case, [a-z][a-z0-9_]{0,63}
	Title       string
	Description string   // required
	ReadOnly    bool     // read keys may call only these tools
	Destructive bool     // meaningful only when ReadOnly is false
	Idempotent  bool
	Roles       []string // if set, the user must have one of these roles
}

// Tool registers a typed tool. It panics on an invalid or duplicate definition
// (a programming error, found at startup).
func Tool[In, Out any](s *Server, def ToolDef, h func(context.Context, In) (Out, error))

func (s *Server) Routes(publicOrigin string) func(*http.ServeMux) // POST /api/mcp, GET MetadataPath
func (s *Server) Command() func(context.Context, []string) error  // `api mcp tools`
func (s *Server) Tools() []ToolDef                                // sorted by name
func (s *Server) SDK() *sdkmcp.Server                             // escape hatch

// ToolError is an error that the model can act on. Its message goes to the model.
func ToolError(format string, args ...any) error
```

3.1. `New` makes one `sdkmcp.Server` for each process. `Tool` wraps `sdkmcp.AddTool`. The SDK infers the input and output schemas from the struct tags (`json`, `jsonschema`).
3.2. The handler is `sdkmcp.NewStreamableHTTPHandler` with `Stateless: true`, `JSONResponse: true`, `MaxRequestBodyBytes: cfg.MaxBodyBytes`, and `PropagateRequestCancellation: true`. The getServer function returns the one server.
3.3. `Routes` registers `POST /api/mcp` (and lets the SDK answer 405 for GET and DELETE on the same path) and `GET /.well-known/oauth-protected-resource/api/mcp`. Go 1.22 patterns: `"/api/mcp"` for the SDK handler, `"GET " + MetadataPath` for the metadata.
3.4. The tool names in `tools/list` are sorted. The order is deterministic.
3.5. A handler error that `ToolError` made becomes a result with `isError: true` and the message as text content. Any other error becomes `isError: true` with the text "The tool failed. Try: retry the call, or report the problem." and a log line at error level with the real error. A panic in a handler becomes the same result and a log line with the stack.
3.6. If the JSON form of a result is larger than `MaxResultBytes`, the result is `isError: true` with the text "The result is too large (N bytes, limit M). Try: ask for a smaller page with limit." The tool handler does not see this.

### 4. Request checks (in this order)

4.1. **Origin.** If the request has an `Origin` header and it is not `publicOrigin` (exact match after lowercasing the scheme and host), answer 403 with a problem body: "The Origin header is not allowed. Try: call the MCP endpoint from a server-side client, or use PUBLIC_ORIGIN."
4.2. **Credential.** The request must carry an API key: `apikeys.KeyFrom(ctx)` is true. A cookie session without a key, or no credential, answers 401 with `WWW-Authenticate: Bearer resource_metadata="<publicOrigin>/.well-known/oauth-protected-resource/api/mcp"` and a problem body: "The MCP endpoint needs an API key. Try: send Authorization: Bearer <key>."
4.3. **Scope and roles, for each tool.** A receiving middleware on the SDK server does this:
- `tools/list`: remove each tool that the caller cannot call. Set `cacheScope` to `"private"` in the result, because the list depends on the caller.
- `tools/call`: if the caller cannot call the tool, answer a JSON-RPC error with code `-32602` (invalid params) and the message "The tool <name> is not available for this key. Try: use a key with the write scope." (or "... for your role." for a role refusal).
- A read key can call a tool only when `ReadOnly` is true. A write key can call every tool. When `Roles` is set, `auth.UserFrom(ctx).HasRole(roles...)` must be true.
4.4. Known gap (follow-up): the auth middleware runs before the MCP handler. A request with an invalid Bearer token gets the 401 of `kit/auth` before the Origin check, and that 401 has no `resource_metadata`. The request is still refused. The fix is an MCP middleware that runs before the auth middleware on `Path`.
4.5. A plain Go error from the SDK path is code `0`. The kit must not return a plain error from the middleware. It uses `*jsonrpc.Error` from `github.com/modelcontextprotocol/go-sdk/jsonrpc`.
4.6. **Body.** After the credential check and before the SDK, the kit reads the body once, bounded by `MaxBodyBytes`. An over-limit body answers 413. A body that is not valid I-JSON (duplicate names, invalid UTF-8) answers 400, the same strictness as ADR 0027. The kit parses the JSON-RPC IDs once for the span. The SDK then reads the same bytes.
4.7. **Deadline.** For protocol versions before 2026-07-28, the SDK does not pass the request cancellation to the tool. The kit restores the request deadline and cancellation around each tool call, so `REQUEST_TIMEOUT` applies to every protocol version.

### 5. The apikeys change: `WithRPCPath`

```go
// WithRPCPath lets a read key send POST to path (an exact match). The handler
// at path must enforce the read scope itself. The quotas still apply.
func WithRPCPath(path string) StoreOption
```

5.1. The middleware still answers 403 for `/api/me/keys*` and `/api/admin/*`.
5.2. The option takes one exact path. It does not take a prefix. An empty path is a no-op.

### 6. The RFC 9728 document

`GET /.well-known/oauth-protected-resource/api/mcp` answers `200`, `Content-Type: application/json`, `Cache-Control: public, max-age=3600`:

```json
{
  "resource": "<publicOrigin>/api/mcp",
  "resource_name": "<cfg.Title or cfg.Name>",
  "bearer_methods_supported": ["header"]
}
```

6.1. It has no `authorization_servers` member until the OAuth ADR. RFC 9728 makes the member optional.
6.2. The route needs no credential. The auth middleware lets it through as an anonymous GET.

### 7. Audit and telemetry

7.1. Before the tool handler runs, the kit sets the audit actor reason to `api_key:<id> mcp:<tool>` (`audit.ActorFrom`, then `audit.WithActor`). An audited write in a tool records this reason.
7.2. Each `tools/call` gets one span, named `tools/call <tool>`, kind SERVER, a child of the otelhttp span. Attributes: `mcp.method.name`, `gen_ai.tool.name`, `gen_ai.operation.name` = `execute_tool`, `mcp.protocol.version`, `jsonrpc.request.id`, and `error.type` on failure. The span does not record arguments or results.
7.3. Each `tools/call` records the histogram `mcp.server.operation.duration` (seconds) with `mcp.method.name`, `gen_ai.tool.name`, and `error.type` on failure.
7.4. These names follow the OTel gen_ai MCP conventions (status "Development" on 2026-11-06). If they change, the kit follows them without a version gate (pre-v1).

### 8. The app command `mcp`

`api mcp tools` prints the tool list as JSON to stdout: name, title, description, the annotations, roles, and the input and output schemas. `api mcp` with no argument or another argument returns a usage error, and the app exits 1, the same as the other app commands. The command does not query the database, but it runs after the app startup, so it needs `DATABASE_URL` like every app command.

### 9. Module, wiring, and doctor check

9.1. Pack `internal/templates/packs/mcp/pack.toml`: `requires = ["auth", "apikeys"]`, no questions.

| Path | Class | Content |
|---|---|---|
| `api/internal/mcp/server.go` | scaffold | `type Deps struct{ DB *pgxpool.Pool; Version string }`, `func New(d Deps) (*kitmcp.Server, error)`: config with the app name and `d.Version`, then `registerWhoami(s)` and `registerGenerated(s, d)` |
| `api/internal/mcp/tools_gen.go` | managed | `func registerGenerated(s *kitmcp.Server, d Deps)` that calls each `register<Name>(s, d)` of the marker list, sorted |
| `api/internal/mcp/whoami.go` | seed | the read-only tool `whoami`: user ID, email, role, key name, key scope, plan name |
| `web/src/routes/.well-known/oauth-protected-resource/[...path]/+server.ts` | managed, `when` the web frontend is on (not embed) | forwards the GET to `API_URL` and returns the answer |

9.2. `main.go.tmpl` (api pack) under `has "mcp"`:
- the import `appmcp "<module>/api/internal/mcp"`;
- `apikeys.WithRPCPath(kitmcp.Path)` as an option of `apikeys.NewStore`; the import `kitmcp "github.com/stopyellingatme/spinup/kit/x/mcp"`;
- `mcpServer, err := appmcp.New(appmcp.Deps{DB: pool, Version: version})` after the key store;
- in `app.New`: `app.WithRoutes(mcpServer.Routes(web.PublicOrigin))` and `app.WithCommand("mcp", mcpServer.Command())`.
9.3. `deploy/Caddyfile.tmpl` under `has "mcp"` and the web frontend: `handle /api/mcp { import upstream.caddy api 8080 { flush_interval -1 } }` before `handle /api/*`. Without a web frontend, Caddy sends every path to the API already, and no block is added.
9.4. The `/settings/api-keys` page (apikeys pack, scaffold) under `has "mcp"`: a card "Connect an AI client" with the endpoint URL (`PUBLIC_ORIGIN` + `/api/mcp`) and copy-ready config for Claude Code (`claude mcp add --transport http <app> <url> --header "Authorization: Bearer <key>"`) and Cursor (`mcp.json` with `headers`).
9.5. `internal/modules/mcp.go`: `MCPNextSteps()` for `spinup module add mcp` (create a key, connect a client, `spinup g mcp tool`).
9.6. Doctor check `mcp-wired` (`internal/doctor/mcpwired`, Local, Quick): it skips when `mcp` is not in `lock.Packs`. It parses `api/cmd/api/main.go` with `go/ast` and requires: a call of `New` from the `api/internal/mcp` import, `.Routes(` on its result inside `app.New`, and `WithRPCPath(` inside the `apikeys.NewStore` call. Each failure names the missing line and ends with a `Try:` line.
9.7. No `[mcp]` table in `spinup.toml`, no kit migration, no drift check. The configuration is Go code in the scaffold `server.go`.

### 10. Generators

10.1. `spinup g mcp tool <name> [--write] [--destructive] [--role <r>]...`
- `<name>` is snake_case. The tool file is `api/internal/mcp/<name>.go` (scaffold). It holds the marker comment `// spinup:mcp-tool register<Camel>` on the line before `func register<Camel>(s *kitmcp.Server, d Deps)`, with typed `In` and `Out` structs and a handler that returns `kitmcp.ToolError("not implemented")`.
- Read-only is the default. `--write` makes a write tool. `--destructive` implies `--write`.
- `--role` values must be declared roles (`config.AuthRoles()`), the same check as `g resource --role`.
- The generator re-renders `tools_gen.go` from the markers in every `api/internal/mcp/*.go` file plus the new one. The list is sorted.
- It fails if `mcp` is not in `lock.Packs` (Try: `spinup module add mcp`) or if the file exists (Try: pass `--force`).
10.2. `spinup g resource <Name> --mcp` also writes `api/internal/mcp/<plural>_tools.go` (scaffold) with the marker `// spinup:mcp-tool register<Plural>Tools`:
- `list_<plural>` (read-only): input `limit` (default 50, maximum 200) and `offset`, the same as the REST list.
- `get_<singular>` (read-only): input `id`.
- `create_<singular>`, `update_<singular>` (write, idempotent for update): the fields of the create and update bodies.
- `delete_<singular>` (write, destructive, idempotent): input `id`.
- Each tool calls the strict-server method of `handlers.NewServer(d.DB)` (for example `ListNotes(ctx, gen.ListNotesRequestObject{...})`) and maps the response object: a 2xx object to `Out`, a 404 or 422 object to `ToolError` with its detail.
- The tools get the `--role` list of the resource as `Roles`. The handler does not check the role, because the route does that.
- `--mcp` fails if `mcp` is not in `lock.Packs`.
10.3. Both generators return a `vfs.Plan`. They register with `Register(...)` in `init()`.

### 11. Errors

Every user-facing error says what failed and why and ends with a `Try:` line. This includes the HTTP problem bodies of section 4 and the tool error texts of section 3.

### 12. Upgrade of existing apps

`spinup module add mcp` adds the module to an app that has `auth` and `apikeys`. The scaffold `main.go` gets the `has "mcp"` blocks through the three-way merge. The `mcp-wired` check finds a merge that lost a line.

### 13. Tests

13.1. `kit/x/mcp`: httptest tests for each check of section 4, the metadata route, the scope filter on `tools/list` and `tools/call`, `cacheScope`, the error mapping, the result size cap, the audit reason, the span and the metric (in-memory exporters), and a panic in a tool.
13.2. `kit/x/apikeys`: a read key POST on the RPC path passes, on another path answers 403, and the quotas count it.
13.3. Golden tree `mcp`. Script tests `mcp_module`, `mcp_module_add`, `mcp_tool`, `mcp_resource`, `mcp_wired`.
13.4. `app_gate_mcp`: generate an app with `--modules auth,apikeys,mcp`, run `g resource Note body:text --mcp`, build and test the app, start the API with `DATABASE_URL`, create a read key and a write key with the operator command, and check every point of section 4 and section 6 with curl.

### 14. Docs

`docs/modules/mcp.md`: enable the module, write a tool, the generators, connect Claude Code, Claude, and Cursor, the scope and role rules, the limits (no OAuth, no ChatGPT, no streams). Add the module to the module table of the concepts page.

## Consequences

- A generated app can expose typed tools to AI clients with one module and the keys it already has.
- The kit gets five new indirect modules. `make vuln` covers them.
- ChatGPT cannot connect until the OAuth ADR.
- A tool that runs longer than `REQUEST_TIMEOUT` fails. The app must move that work to a job.

## Appendix A: Units

### U0 (main loop): ADR and pre-seed
This ADR, the go-sdk pin (kit and app lockfiles, golden copies, `stackversions.yaml`, `kit/internal/deps/deps_mcp.go`), the packs digest, the CLAUDE.md line, the stubs, and the `wire_gen.go` imports.

### K1: `kit/x/mcp`
Files: `kit/x/mcp/**`. Check: `T ./kit/x/mcp/...`. Sections 3, 4, 6, 7, 8, 13.1.

### K2: `WithRPCPath`
Files: `kit/x/apikeys/store.go`, `kit/x/apikeys/middleware.go`, and their tests. Check: `T ./kit/x/apikeys/...`. Section 5, 13.2.

### P1: pack and wiring (after K1, K2)
Files: `internal/templates/packs/mcp/**`, the `has "mcp"` blocks in `packs/api/files/api/cmd/api/main.go.tmpl`, `packs/deploy/files/deploy/Caddyfile.tmpl`, the settings page of the apikeys pack, `internal/modules/mcp.go`, `internal/templates/golden_mcp_test.go`, `testdata/golden/mcp/**`, the golden trees that change, `internal/templates/packs.digest`. Check: `G mcp`, `G apikeys`, `G deploy`, `G api`, `T ./internal/modules/...`. Section 9.1 to 9.5.

### G1: generators (after P1)
Files: `internal/gen/mcp/**`, the `--mcp` flag in `internal/gen/resource/`, `testdata/script/mcp_tool.txtar`, `testdata/script/mcp_resource.txtar`. Check: `T ./internal/gen/mcp/... ./internal/gen/resource/...`, `S mcp_tool`, `S mcp_resource`. Section 10.

### D1: doctor, docs, scripts, gate (after P1, G1)
Files: `internal/doctor/mcpwired/**`, `docs/modules/mcp.md`, the concepts module table, `testdata/script/mcp_module.txtar`, `mcp_module_add.txtar`, `mcp_wired.txtar`, `app_gate_mcp.txtar`. Check: `T ./internal/doctor/mcpwired/...`, `make doctest`, `S mcp_*`. Sections 9.6, 13.3, 13.4, 14.

### Main loop after the units
Merge, the final gate (`make build lint test golden script doctest`, `app_gate_mcp`, `app_gate_apikeys`, `make vuln`), and a dogfood check with Claude Code against the todo app.
