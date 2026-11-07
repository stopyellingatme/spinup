---
title: generate mcp — add a typed MCP tool
parent: Command guides
---

# generate mcp — add a typed MCP tool

`spinup g mcp tool <name>` adds one typed tool to the MCP server of your app.
The app needs the `mcp` module, which needs the `auth` and `apikeys` modules.

```console
$ spinup new mcpapp --module example.com/mcpapp --modules ci,auth,apikeys,mcp --no-git --non-interactive
...
$ cd mcpapp
$ spinup g mcp tool lookup_order
...
$ grep -c "spinup:mcp-tool registerLookupOrder" api/internal/mcp/lookup_order.go
1
```

The command writes `api/internal/mcp/<name>.go` with typed `In` and `Out` structs and a handler that you fill in.
It also rewrites `api/internal/mcp/tools_gen.go`, which registers every tool file that has a `spinup:mcp-tool` marker.

The name must be snake_case. The names `generated`, `whoami`, and `server` belong to the module, so the command refuses them.

## Flags

| Flag | What it does |
| --- | --- |
| (none) | A read-only tool. A read key and a write key can both call it. |
| `--write` | A write tool. Only a write key can call it. |
| `--destructive` | A write tool that can remove data. It implies `--write`. Clients can ask the user before they call it. |
| `--role <role>` | Only a user with one of these roles can see and call the tool. Repeat the flag for more roles. Each role must be declared in `[auth]`. |
| `--force` | Replace a tool file that exists. |

## Tools for a resource

`spinup g resource <Name> --mcp` writes the list, get, create, update, and delete tools of a resource.
See [Resources](generate-resource.md#add-mcp-tools).

See [MCP server](../build/mcp.md) for the full guide.
