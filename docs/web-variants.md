# Select the web tools

Select the package manager and the API client when you create the app.
The lock records both answers.
The defaults remain `pnpm` and `openapi-fetch`.
Both options support local builds with `--frontend-mode node` and `--frontend-mode embed`.
The embed container still needs a Bun branch in `deploy/Dockerfile.api`.
That template belongs to the API pack and is outside this unit's permitted files.
Embed mode retains its restrictions on auth and server pages.

```console
$ spinup new variants --module example.com/variants --package-manager bun --api-client heyapi --no-git --non-interactive
...
$ cd variants
$ test -f web/bun.lock
$ test ! -f web/pnpm-lock.yaml
$ grep '"packageManager": "bun@' web/package.json
...
$ grep 'openapi-ts --input' web/package.json
...
$ spinup g client --dry-run
$ spinup g component dialog --dry-run
...
$ test ! -d web/node_modules
```

Use the Bun version from `stackversions.yaml` (`npm.bun`).
CI and Docker install that exact version with `npm install --global bun@<version>`.
The repository does not pin `oven-sh/setup-bun`.
The npm installation avoids an unverified shell script from the network.
The application server still uses Node 24 in node mode.

Run `bun install --frozen-lockfile` in `web/`.
Use a populated Bun cache when the environment has no network access.
Bun 1.3 has no `--offline` option.
The variant tests copy cached packages into a writable cache selected with `BUN_INSTALL_CACHE_DIR`.
A cache miss requires the missing package before the install can complete.
For pnpm, run `pnpm install --frozen-lockfile --offline`.

Run `bun run check` to check a Bun app.
Run `bun run build` to build it.
`spinup dev` uses `bun run dev` for Vite.
The generator commands use `bunx --no-install` for installed binaries.
The component generator uses `bunx` for the selected shadcn registry.
The upgrade step refreshes `bun.lock` with `bun install --lockfile-only`.
The quick doctor check tests the selected package manager.
Its stable check ID remains `pnpm` for compatibility.

Run `spinup g client` after an OpenAPI change.
For heyapi, this command runs `openapi-ts` and writes `web/src/lib/api/client/`.
It also generates `web/src/lib/api/schema.d.ts` with `openapi-typescript`.
These shared types preserve compatibility with the resource pages.
Commit both outputs with the OpenAPI bundle.
The full doctor check compares every generated file and the shared types.
It detects changed, missing, and obsolete files without changing the app.

The scaffold at `web/src/lib/api/client.ts` imports the generated heyapi client.
Its `api` export preserves the typed `GET`, `POST`, and other methods used by resource pages.
These methods use heyapi for requests and retain SvelteKit's `fetch` for cookies and server calls.
Use the `client` export or the generated SDK for native heyapi calls.
The pack includes generated seeds, so a new app can pass its checks before client generation.
