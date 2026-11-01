---
title: Package manager and API client
parent: Build your app
nav_order: 2
---

# Package manager and API client

You choose two tools for the web app when you create it.
The package manager (`pnpm` or `bun`) installs the packages and runs the scripts in `web/`.
The API client (`openapi-fetch` or `heyapi`) is the code that your pages use to call the Go API.
The defaults are `pnpm` and `openapi-fetch`. Pick `bun` or `heyapi` if your team prefers them.

```console
$ spinup new todo --module example.com/todo --package-manager bun --api-client heyapi --no-git --non-interactive
...
$ cd todo
$ ls web/bun.lock
web/bun.lock
```

## The choices

| Flag | Values | Default | What changes |
| --- | --- | --- | --- |
| `--package-manager` | `pnpm`, `bun` | `pnpm` | The lockfile (`pnpm-lock.yaml` or `bun.lock`) and the commands in `web/` |
| `--api-client` | `openapi-fetch`, `heyapi` | `openapi-fetch` | How `spinup g client` makes the typed client |

Both flags work with `--frontend-mode node` and `--frontend-mode embed`.
The lock records your answers, so `spinup dev`, `spinup build`, and `spinup upgrade` use the same tools.

{: .note }
Choose before you create the app. spinup has no command to switch an existing app.
To change tools, make a new app and copy your `web/src` files into it.

## Daily commands

Run these in `web/`.

| Task | pnpm | bun |
| --- | --- | --- |
| Install | `pnpm install` | `bun install` |
| Dev server | `pnpm dev` | `bun run dev` |
| Type check | `pnpm check` | `bun run check` |
| Unit tests | `pnpm test` | `bun run test` |
| Build | `pnpm build` | `bun run build` |
| Install in CI | `pnpm install --frozen-lockfile` | `bun install --frozen-lockfile` |

`spinup dev` and `spinup build` pick the right tool for you.
The server in node mode still runs on Node 24, also with bun.

## Regenerate the client

After you change the API, run:

<!-- doctest: skip -->
```console
$ spinup g openapi
$ spinup g client
```

With `openapi-fetch`, this writes `web/src/lib/api/schema.d.ts`.
With `heyapi`, it writes the client in `web/src/lib/api/client/` and the same `schema.d.ts`.
While `spinup dev` runs, saving a file in `api/openapi/` runs both commands for you.
Commit the generated files with the OpenAPI bundle.
A new app already holds generated files, so its checks pass before you run `spinup g client`.

## Call the API from code

The `api` export in `web/src/lib/api/client.ts` has the same shape for both clients, so resource pages work with either.
In server code, pass the `fetch` of the load function and `__PUBLIC_ORIGIN__`, as in [The web app](web.md#call-the-api-from-a-page):

```ts
import { api } from '#lib/api/client';

const { data } = await api.GET('/api/todos', { fetch, baseUrl: __PUBLIC_ORIGIN__ });
```

For the native heyapi functions, import the generated SDK or the `client` object:

```ts
import { client } from '#lib/api/client';
import { listTodos } from '#lib/api/client/sdk.gen';

// In server code, add fetch and baseUrl as above.
const { data } = await listTodos({ client });
```

The function names come from the `operationId` values in your OpenAPI files.

## Offline and CI notes

- With pnpm, install without the network by using `pnpm install --frozen-lockfile --offline`. The package store must hold the packages.
- Bun has no `--offline` option. Fill the Bun cache first, or use the network.
- The generated CI and Dockerfiles install the Bun version that spinup pins.
- `spinup doctor` checks that the chosen package manager is installed.
- The full `spinup doctor` also finds a client that is out of date. It compares the generated files with a fresh run and changes nothing.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `bun: command not found` | Bun is not installed | Install Bun, then run `spinup doctor` |
| The `api` calls fail to type check after an API change | The client is old | Run `spinup g openapi`, then `spinup g client` |

## See also

- [The web app](web.md)
- [Embed mode](embed.md)
- [generate client](commands/generate-client.md)
- [new](commands/new.md)
