# ADR 0002: SvelteKit version for the web pack

Status: accepted (2026-10-13)

## Decision

Use **SvelteKit 3.0.1** (stable on the npm `latest` tag since 2026-10-01) with adapter-node 6.0.0,
Svelte 5.57.2, Vite 8.3.4, vite-plugin-svelte 7.3.1, TypeScript 6.0.3, and pnpm 12.11.2.
SvelteKit 2 is not needed as a fallback.

## Evidence (Step 0 spike, Node 24.21.0, a Go net/http API on :8080)

| Check | Result |
|---|---|
| cookie-ssr: the Go session cookie set through a form action survives SSR | PASS |
| form-action: same-origin POST is accepted, a cross-site or missing Origin gets 403 | PASS |
| paths-origin: `paths.origin` controls the CSRF origin | PASS |
| traceparent: the SvelteKit server sends `traceparent` to Go | PASS, with the undici instrumentation |
| docker-build: a 2-stage `node:24-slim` image builds and serves | PASS (406 MB) |

## Rules for the web pack (M4.1, M6.4)

1. Kit 3 has no `svelte.config.js`. Put `adapter`, `paths`, `csrf`, and `tracing` in the `sveltekit({...})` options in `vite.config.js`. `sveltekit()` returns `Promise<Plugin[]>`.
2. adapter-node 6 does **not** read the `ORIGIN` environment variable. It bakes `paths.origin` at build time. The public URL is a build input: `Dockerfile.web` takes `ARG PUBLIC_ORIGIN`. `spinup.toml` is its source.
3. Without `paths.origin`, adapter-node 6 assumes `https` when `PROTOCOL_HEADER` is not set. Always set `paths.origin`.
4. `tsconfig.json` must have `"extends": "$app/tsconfig"`. Add the script `"prepare": "svelte-kit sync"`. In Docker, run `pnpm exec svelte-kit sync` because `--ignore-scripts` skips `prepare`.
5. `csrf.checkOrigin` was removed in 3.0. Use `csrf.trustedOrigins`. CSRF checks run only in production builds. A POST with no `Origin` is rejected, so tests send `-H 'Origin: …'`.
6. `handleFetch` must match the same public origin string as `paths.origin`. Use one source for both.
7. A form action must re-parse the Go `Set-Cookie` with `res.headers.getSetCookie()` and pass it to `cookies.set(name, value, {path, httpOnly, sameSite, maxAge})`.
8. Tracing: `tracing.server = true` plus `NodeSDK` is not enough. Add `@opentelemetry/instrumentation-undici`, or fetch to Go carries no `traceparent`.
9. pnpm 12 fails with `ERR_PNPM_IGNORED_BUILDS` for `protobufjs` (pulled in by the OTel exporter). `onlyBuiltDependencies: []` does not fix it. Generate `pnpm-workspace.yaml` with `allowBuilds: { protobufjs: false }`.
10. Pin TypeScript 6.x. TypeScript 7 breaks the kit and svelte-check peer ranges.

## Not tested in the spike

shadcn-svelte, bits-ui, vitest, svelte-check, Playwright, `vite dev`, and a real OTLP backend. M4.1 and M4.7 cover them.
The spike source is in the session scratchpad (`step0/sveltekit-spike/`) and is not part of the repository.
