# ADR 0034: Web performance and the performance gate

Status: accepted (owner, 2026-11-13)
Date: 2026-11-13
Extends: ADR 0008 (web layout), ADR 0014 (embed mode), ADR 0027 (JSON v2 and the perf loop)

## Context

On 2026-11-13 we compared spinup with corten, a Rust web framework of a co-worker.
Corten serves precompressed assets with an `ETag`, measures the cost of each default, and names the commit of each number it cites.
A study of spinup on the same axes found these facts:

- Node mode already precompresses. `@sveltejs/adapter-node` 6 has `precompress` on by default and serves `.br` and `.gz` files with an `ETag`.
- Caddy serves no files. Every route in the Caddyfile is a `reverse_proxy`.
- Embed mode is the gap. `kit/app/spa.go` copied each file on every request, and sent no `ETag` and no compressed variant. `docs/embed.md` runs the binary with no Caddy in front.
- `make perf` had no web metrics, no committed baseline, and no gate in CI.
- ADR 0027 measured a buffered `WriteJSON` as slower than `json.Marshal` for the list response.
- The root layout returns the signed-in user, so `prerender` is not possible in node mode.

## Decision

### 1. Static assets in embed mode

1. The embed build sets `precompress: true` in `adapter-static`. The build output holds a `.br` and a `.gz` sibling for each compressible file.
2. `app.WithSPA` builds an index one time, when it registers its route. The index records each file and its siblings.
3. `WithSPA` serves a file with `fs.Open` and `http.ServeContent`. It does not copy the file for each request.
4. Negotiation: `br` first, then `gzip`, then the plain file. A coding with `q=0` is refused. The function is `httpx.AcceptsEncoding(header, coding string) bool`. `kit/apidocs` uses the same function.
5. An encoded answer sets `Content-Encoding`, `Vary: Accept-Encoding`, and the content type of the original name.
6. `ETag`: files outside `_app/immutable/` get a strong `ETag` from a SHA-256 of the plain content, with the suffix `.br` or `.gz` for an encoded variant. `If-None-Match` gives `304`. Files under `_app/immutable/` get no `ETag`, because a browser never revalidates them. The hash is computed at registration, only for the files outside `_app/immutable/`.
7. Cache headers stay as they are: `no-cache` for `index.html` and other files, `public, max-age=31536000, immutable` under `_app/immutable/`.
8. The CSP of `index.html` no longer has `'unsafe-inline'` for scripts. `WithSPA` reads `index.html` one time, computes the SHA-256 of each inline `<script>` body, and sends `script-src 'self' 'sha256-...'` (ADR 0036 section 2).

### 2. Node mode

The adapter-node defaults are the contract. spinup sets no option. The `web` perf suite asserts that an immutable asset answers with `content-encoding: br` and that a second request with `If-None-Match` answers `304`.

### 3. Cache defaults in the kit

1. The kit sets `Cache-Control: private, no-cache` on a response when the handler set no `Cache-Control`. The middleware is part of the default chain in `kit/app`.
2. `httpx.PublicCache(w, maxAge time.Duration)` is the opt-in for a shareable answer. It sets `Cache-Control: public, max-age=<seconds>`.
3. `WithSPA` and `kit/apidocs` set their own values. Their values win.

### 4. Not done, with the reason

| Item | Reason |
|---|---|
| `prerender` | The root layout needs the session. |
| `precompress` option for adapter-node | It is the default. |
| Caddy `precompressed` | Caddy serves no files. |
| Pooled encoder in `WriteJSON` | ADR 0027 measured it as slower. A benchmark and a budget guard it. |
| `ETag` for dynamic JSON, zstd in the kit | The handler and the query still run. Response buffering breaks streams. Caddy keeps `encode zstd gzip`. |
| Unix socket or a custom agent for the SvelteKit-to-API hop | Node `fetch` already pools connections. The `web` suite measures the hop first. |

### 5. The bundle budget

1. The doctor check `web-bundle` (package `internal/doctor/webbundle`, local, not quick) reads `web/build` (node mode) or `web/build` of the embed build. It skips when the directory does not exist.
2. It sums the bytes of the client JavaScript and CSS, raw and as the `.br` siblings when they exist.
3. Built-in limits apply. An optional `web/bundle-budget.json` (`{"client_br_bytes": N, "client_raw_bytes": N}`) overrides them. The file is the owner's file. No pack ships it.
4. Above a limit the status is `warn`, with a `Try:` line.

### 6. Measurement contract

1. Suites of `scripts/perf/perf.py`: `cli`, `app`, `micro`, `web` (default set), and `dev` (opt-in, `SUITES=dev`).
2. Metric classes: `primary` (gated by `perf-compare` at 10 percent), `informational` (latency percentiles, time to first byte), and `deterministic` (bytes, allocations for each operation, request counts). A deterministic metric compares on any machine.
3. A suite runs its conformance checks before it times anything. A failed conformance check stops the suite. It never records a zero.
4. The `web` suite records: `web_ssr_rps_page` (a signed-in list page), `web_ssr_rps_login` (anonymous), `web_build_time`, `web_start_to_healthz`, `web_idle_rss`, `web_rss_after_load`, `web_bundle_raw_bytes`, `web_bundle_br_bytes`, `web_bundle_gz_bytes`, `web_first_load_br_bytes_login`, `web_api_requests_per_page`, `web_ttfb_page`, and in the embed phase `embed_binary_size` and `embed_index_rps`.
5. The `dev` suite records the edit-to-response times. It is the Go half of `scripts/bench/suite_dev.py` of the branch `bench`.
6. The load generator `scripts/perf/loadgen` gains `ttfb_ms`, `-header`, `-expect-body`, and `bytes_received`. It uses only the standard library.

### 7. Baseline and receipts

1. `perf.py baseline <result.json>` writes `perf/baseline/<cpu-slug>.json` and `<cpu-slug>.md`. `make perf-baseline NEW=<result.json>` runs it.
2. It refuses a dirty tree, a quick run, and a run where a load gate timed out.
3. The file keeps the commit sha, the CPU, the cores, the RAM, the OS, and the Go version. It drops `hostname` and the repository path, because the repository is public.
4. A number cited in the docs names its commit.

### 8. The gate

1. Timing: `make perf-compare` against the committed baseline of the same machine, before a merge that changes the kit or the web pack. It is a local step. Hosted runners vary by 20 percent or more, so CI does not time anything.
2. Deterministic, in CI: `make perf-gate` runs the `Test*Budget` tests without the race detector, as one step of the existing `test` job. A budget test uses `testing.AllocsPerRun`, shares the body of its benchmark, and holds the ceiling as a constant. It skips in a race build.
3. Deterministic, nightly: the e2e run already builds the web app. `scripts/perf/bundle.py check` compares the bundle bytes with `perf/budgets.json` there.

## Consequences

- The embed binary grows by the size of the compressed copies. `embed_binary_size` reports it.
- A response with no `Cache-Control` now has `private, no-cache`. The project has no compatibility ceremony before v1.
- A Go release can change allocation counts. The bump updates the ceilings in the same commit.
- Work on the SvelteKit-to-API hop waits for the numbers of the `web` suite.

## Unit file sets

See `docs/design/plan.md`, section "N1".
