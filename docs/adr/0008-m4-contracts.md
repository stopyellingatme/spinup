# ADR 0008: M4 contracts (dev loop and frontend)

Status: accepted (2026-10-15)

The M4 units run in parallel. These decisions fix the contracts between them. ADR 0002 (SvelteKit 3, ten web-pack rules) stays binding.

## 1. Tooling

- pnpm runs through corepack at the version pinned in `stackversions.yaml`: `scripts/bin/pnpm` (the Makefile puts it on `PATH`). The pnpm store holds every package of the web lockfile, so `pnpm install --frozen-lockfile --offline` works in lanes.
- The main loop owns the web pack's `pack.d/deps.toml` (`web/package.json`, `web/pnpm-lock.yaml`, `web/pnpm-workspace.yaml`) and `pack.d/shadcn.toml` (the shadcn-svelte 1.7.0 components, the output of `shadcn-svelte add button card input label`). `pnpm-lock.yaml` is `seed`: upgrades never merge it; the dependency step reinstalls.
- Kit 3 uses `#lib/...` subpath imports (`package.json` `imports`). shadcn-svelte 1.7.0 understands `#lib` but not `$lib` in a Kit 3 project, so every alias in `components.json` uses `#lib`.
- Known peer warnings: `runed` (through bits-ui) wants Kit 2; `openapi-typescript` 7.13 wants TypeScript 5. Both work; M4.2 proves the client generation with TypeScript 6.

## 2. Web app layout (pack `web`, unit M4.1)

| Path | Class | Notes |
|---|---|---|
| `web/vite.config.js` | managed | `sveltekit({adapter, paths: {origin}, csrf: {trustedOrigins}})` (ADR 0002 rules 1 to 6), Tailwind, and the dev proxy `/api` to `API_URL` |
| `web/tsconfig.json` | managed | `"extends": "$app/tsconfig"` |
| `web/components.json` | scaffold | shadcn-svelte config with `#lib` aliases |
| `web/src/app.html`, `web/src/app.css`, `web/src/app.d.ts` | scaffold | `app.css` imports Tailwind and `tw-animate-css` and holds the shadcn theme variables (base color neutral); `app.d.ts` declares `App.Locals { user: User \| null }` |
| `web/src/hooks.server.ts` | scaffold | `handle` loads the user from `GET ${API_URL}/api/session`; `handleFetch` rewrites `${PUBLIC_ORIGIN}/api/*` to `${API_URL}/api/*` and copies the cookie header for that host only |
| `web/src/lib/utils.ts` | scaffold | `cn`, `WithElementRef`, and the helper types the shadcn components import from `#lib/utils.js` |
| `web/src/lib/api/client.ts` | scaffold | an `openapi-fetch` client typed by `schema.d.ts` |
| `web/src/lib/api/schema.d.ts` | seed | an empty `paths` interface at first; the `client` post-step overwrites it (tool output) |
| `web/src/routes/+layout.svelte`, `+layout.server.ts`, `+page.svelte` | scaffold | the shell and a home page; the layout passes `locals.user` to pages |
| `web/src/routes/healthz/+server.ts` | managed | returns `{"status":"ok"}` |
| `deploy/Dockerfile.web` | managed | Node 24 image, `ARG PUBLIC_ORIGIN` (ADR 0002 rule 2), `pnpm exec svelte-kit sync` (rule 4) |

- Environment: `PUBLIC_ORIGIN` (default `http://localhost:5173`) is the single origin source for `paths.origin`, `csrf.trustedOrigins`, and `handleFetch` (rule 6). `API_URL` (default `http://localhost:8080`) is where server-side code reaches Go.
- The web pack adds the question `shadcn_registry` (string, default `1.7.0`), so the lock records the component registry version (critic resolution 6). The web pack is on unless `new --api-only`.

## 3. Session and auth (kit `auth`, pack `auth`, unit M4.3)

- `GET /api/session` returns 200 `{"user": {"id": <int64>, "email": "..."}}`, or 401 problem details. Without the auth module the route does not exist (404), and the web treats that as no user.
- `POST /api/auth/register` and `POST /api/auth/login` take `{"email","password"}` and set the session cookie (the session token is rotated on login). `POST /api/auth/logout` returns 204 and clears it.
- Cookie: name `session`, `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` unless `SPINUP_ENV=dev`.
- Kit `auth` wraps scs with the pgxstore store (table `sessions`), hashes passwords with argon2id, locks an account after repeated failures, rejects unsafe cross-site requests (`Sec-Fetch-Site: cross-site`, or a foreign `Origin`), and sets a default CSP. The auth pack ships its migration, wires kit `auth` into `api/cmd/api/main.go` (only when the auth module is on; apps without it keep byte-identical output), and adds the web pages `login`, `register`, and a `logout` action. Form actions re-set the Go cookie with `getSetCookie()` (rule 7).

- **Amendment (2026-10-22):** the status lists of the auth routes now include 429. `POST /api/auth/register`, `POST /api/auth/login`, `POST /api/auth/reset/request`, and `POST /api/auth/reset/confirm` answer 429 with `Retry-After` when a client is over its rate limit. `GET /api/auth/oidc/{provider}/callback` answers a 303 redirect to `/login?error=rate_limited` in that case. For login, 429 also means the lockout of one email. The two cases have the same status and `type`, and they differ in the `detail` text. ADR 0016 has the contract.

## 4. Client generation (unit M4.2)

`spinup g client` and the `client` post-step run `pnpm --dir web exec openapi-typescript ../api/openapi/bundle.gen.yaml -o src/lib/api/schema.d.ts` when the app has a web pack and a bundle. The resource generator requests `client` when the web pack is present (unit M4.6). The `client-drift` doctor check compares a fresh generation with the file on disk.

## 5. Dev loop (unit M4.4)

`spinup dev`: `doctor --quick` checks, `docker compose -f deploy/compose.dev.yml up -d --wait` (with `--lgtm`, also `grafana/otel-lgtm`), migrations through the app binary, `go tool -modfile=tools.mod air` in `api/` (the api pack gains a managed `api/.air.toml` through `pack.d/air.toml`), and `pnpm --dir web dev` with `API_URL` and `PUBLIC_ORIGIN` set. Busy ports move to free ones, recorded in `.spinup/dev.json`. A change in `api/db/queries/*.sql` runs sqlc; a change in `api/openapi/**/*.yaml` runs the `openapi` and `client` post-steps.

## 6. Devtools (unit M4.5)

Kit `devtools` serves `/_dev` only when `SPINUP_ENV=dev`. Kit `app` mounts it itself, so no scaffold file changes.
