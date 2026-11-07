---
title: MCP server
parent: Build your app
nav_order: 19
---

# MCP server

The `mcp` module gives your API an endpoint for AI clients that use the Model Context Protocol (MCP).
Claude, Claude Code, Cursor, and other agents can then call typed tools in your app.
A client authenticates with an API key. The module needs the `auth` and `apikeys` modules.

```console
$ spinup new todo --module example.com/todo --modules auth,apikeys,mcp --no-git --non-interactive
...
$ cd todo
$ spinup doctor --quick | grep mcp-wired
ok	mcp-wired	mcp wiring is present
```

## How the endpoint works

The endpoint is `POST /api/mcp`. It is under `/api/`, so Caddy sends it to the Go API in every frontend mode.
The server is stateless. Each request is one POST, and each answer is one JSON document.
Any replica can answer any request, so blue/green deploys need no sticky sessions.
`GET` and `DELETE` on `/api/mcp` answer 405.

The API checks each request in this order:

| Step | Check | Answer on failure |
| --- | --- | --- |
| 1 | If the request has an `Origin` header, it must be `PUBLIC_ORIGIN`. | 403 with a problem body |
| 2 | The request must carry an API key in `Authorization: Bearer <key>`. A cookie session is not enough. | 401 with `WWW-Authenticate: Bearer resource_metadata="..."` |
| 3 | The key and the user must be able to call the tool. | JSON-RPC error `-32602` |

One HTTP request uses one request of the [plan quota](../build/api-keys.md#declare-plans).
The route `GET /.well-known/oauth-protected-resource/api/mcp` serves the RFC 9728 metadata. It needs no credential.

## Enable the module

Add the module to a new app with `--modules auth,apikeys,mcp`, as the first example shows.
To add it to an existing app that has `auth` and `apikeys`, run `spinup module add`. The command makes a branch for you to review.

<!-- doctest: skip -->
```console
$ spinup module add mcp --yes --non-interactive
...
Next: git merge spinup/module-mcp
```

The module adds these files:

| Path | Class | What it holds |
| --- | --- | --- |
| `api/internal/mcp/server.go` | scaffold | `New`, which makes the server and registers the tools. Change the name, the title, or the instructions here. |
| `api/internal/mcp/tools_gen.go` | managed | `registerGenerated`, which calls the tools that the generators write. |
| `api/internal/mcp/whoami.go` | seed | The tool `whoami`. |
| `web/src/routes/.well-known/oauth-protected-resource/[...path]/+server.ts` | managed | Forwards the metadata request to the API. It is not in embed mode. |

The module also adds these lines to `api/cmd/api/main.go`. The three-way merge adds them to an app that you changed.

```go
keyStore, err := apikeys.NewStore(pool, appapikeys.Config, apikeys.WithRPCPath(kitmcp.Path))
// ...
mcpServer, err := appmcp.New(appmcp.Deps{DB: pool, Version: version})
if err != nil {
	return err
}
// ...in app.New:
app.WithRoutes(mcpServer.Routes(web.PublicOrigin)),
app.WithCommand("mcp", mcpServer.Command()),
```

`apikeys.WithRPCPath` lets a read key send a POST to `/api/mcp` only. The MCP server then applies the read scope to each tool.
The doctor check `mcp-wired` finds a missing line. It names the line and shows it in the `Try:` line.

## The whoami tool

The module starts with one read-only tool, `whoami`. It returns the user ID, the email, the role, the key name, the key scope, and the plan name.
Use it to make sure that a client connects with the correct key.
The file is a seed file. Change it or delete it, and spinup does not put it back.

The app command `mcp tools` prints every tool as JSON, with its schemas. It needs no database.

<!-- doctest: skip -->
```console
$ cd api
$ go run ./cmd/api mcp tools
[
  {
    "name": "whoami",
    "description": "Returns the authenticated user and API key.",
    ...
```

## Write a tool

A tool is a typed Go function. Run `spinup g mcp tool <name>` to write one.
The name is snake_case. The command writes `api/internal/mcp/<name>.go` and updates `tools_gen.go`.

```console
$ spinup g mcp tool lookup_order
Created 1, updated 1 files
$ grep -c registerLookupOrder api/internal/mcp/tools_gen.go
1
```

The new file has an `In` struct for the arguments, an `Out` struct for the result, and a handler that returns `kitmcp.ToolError("not implemented")`.
Add fields with `json` and `jsonschema` tags. The kit makes the input and output schemas from these tags.

```go
// LookupOrderIn holds the tool arguments.
type LookupOrderIn struct {
	ID int64 `json:"id" jsonschema:"The order number"`
}

// LookupOrderOut holds the tool result.
type LookupOrderOut struct {
	Status string `json:"status"`
}

// spinup:mcp-tool registerLookupOrder
func registerLookupOrder(s *kitmcp.Server, d Deps) {
	kitmcp.Tool(s, kitmcp.ToolDef{
		Name:        "lookup_order",
		Description: "Returns the status of one order.",
		ReadOnly:    true,
	}, func(ctx context.Context, in LookupOrderIn) (LookupOrderOut, error) {
		var status string
		err := d.DB.QueryRow(ctx, "SELECT status FROM orders WHERE id = $1", in.ID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return LookupOrderOut{}, kitmcp.ToolError("The order %d does not exist. Try: use an existing order number.", in.ID)
		}
		return LookupOrderOut{Status: status}, err
	})
}
```

Keep the marker comment `// spinup:mcp-tool register<Name>`. The generators read it to write `tools_gen.go`.

| Flag | What it does |
| --- | --- |
| `--write` | Makes a write tool. Only a write key can call it. |
| `--destructive` | Marks the tool as destructive. It implies `--write`. |
| `--role <role>` | Lets only users with this role call the tool. Repeat the flag for more roles. The role must be in `[auth]` of `spinup.toml`. |
| `--force` | Writes the file again when it exists. |

A tool is read-only by default.
A tool error from `kitmcp.ToolError` goes to the model as text, so the model can correct its call.
Any other error goes to the model as "The tool failed. Try: retry the call, or report the problem." The API logs the real error.
A result larger than 256 KiB is an error. Return a page of records, with a `limit` argument.

## Tools for a resource

Add `--mcp` to `spinup g resource` to write five tools for the resource.
The tools call the same handlers as the REST routes, so they use the same validation and the same owner checks.

```console
$ spinup g resource note body:text --mcp --dry-run | grep mcp
create api/internal/mcp/notes_tools.go (scaffold)
update api/internal/mcp/tools_gen.go (managed)
```

| Tool | Kind | Arguments |
| --- | --- | --- |
| `list_notes` | read-only | `limit` (default 50, maximum 200) and `offset` |
| `get_note` | read-only | `id` |
| `create_note` | write | the fields of the create body |
| `update_note` | write, idempotent | `id` and the fields of the update body |
| `delete_note` | write, destructive, idempotent | `id` |

If you pass `--role` to `g resource`, the tools get the same roles.
`--mcp` fails when the app has no `mcp` module. The error shows `Try: spinup module add mcp`.

## Connect a client

Make a key on the page `/settings/api-keys`, or with the [app command](../reference/app-commands.md#apikeys).
With the `mcp` module, the page shows the card "Connect an AI client" with the endpoint URL and the configuration to copy.
In the examples, `https://app.example.com` is your `PUBLIC_ORIGIN`, and `todo_Q3n8...` is the key.

### Claude Code

<!-- doctest: skip -->
```console
$ claude mcp add --transport http todo https://app.example.com/api/mcp --header "Authorization: Bearer todo_Q3n8..."
```

### Claude

Open the connector settings and add a custom connector.
Set the URL to `https://app.example.com/api/mcp`. Choose "No sign-in", and add the header `Authorization` with the value `Bearer todo_Q3n8...`.

### Cursor

Add the server to `mcp.json`, in `.cursor/` of your project or in your home folder:

```json
{
  "mcpServers": {
    "todo": {
      "url": "https://app.example.com/api/mcp",
      "headers": {
        "Authorization": "Bearer todo_Q3n8..."
      }
    }
  }
}
```

### Test with curl

A client sends `Accept: application/json, text/event-stream` and an MCP protocol version.

<!-- doctest: skip -->
```console
$ curl -sS -X POST https://app.example.com/api/mcp \
    -H "Authorization: Bearer todo_Q3n8..." \
    -H "Content-Type: application/json" \
    -H "Accept: application/json, text/event-stream" \
    -H "MCP-Protocol-Version: 2025-11-25" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami","arguments":{}}}'
{"jsonrpc":"2.0","id":1,"result":{"content":[...],"structuredContent":{"user_id":1,"email":"ada@example.com",...}}}
```

## Scopes and roles

A key acts as its user. The scope of the key and the role of the user decide which tools the client sees and calls.

| Key scope | `tools/list` shows | `tools/call` accepts |
| --- | --- | --- |
| `read` | Only the read-only tools | Only the read-only tools |
| `write` | Every tool | Every tool |

If a tool has `Roles`, the user must have one of these roles. Other users do not see the tool, and a call fails.
A refused call answers the JSON-RPC error `-32602` with one of these messages:

- `The tool <name> is not available for this key. Try: use a key with the write scope.`
- `The tool <name> is not available for your role. Try: use an account with a required role.`

The tool list depends on the caller, so the server marks it as private (`"cacheScope":"private"`).
Give a client a read key when it only reads. Give it a write key only when it must change data.

With the [audit module](../audit.md), a write in a tool records the reason `api_key:<key id> mcp:<tool>`.
Each tool call also makes one trace span, `tools/call <tool>`, and one value of the metric `mcp.server.operation.duration`.
The span does not record the arguments or the result.

## Limits

The module does not do these things:

- It does not support OAuth. A client must send an API key in the `Authorization` header.
- ChatGPT cannot connect, because it accepts only OAuth.
- It does not send streams. The server answers each request with one JSON document, with no SSE progress and no `subscriptions/listen`.
- It does not give helpers for MCP prompts and resources. Use `SDK()` on the server for these.
- A tool call has the same time limit as a REST request: `REQUEST_TIMEOUT`, 30 seconds by default. Move long work to a [background job](../build/jobs.md), and return the job ID from the tool.

## See also

- [API keys and plans](../build/api-keys.md): the keys, the scopes, and the quotas
- [Roles](../build/roles.md): the roles that a tool can require
- [Doctor checks](../reference/doctor-checks.md): `mcp-wired`
- [ADR 0031](https://github.com/stopyellingatme/spinup/blob/main/docs/adr/0031-mcp-server.md): the decisions
