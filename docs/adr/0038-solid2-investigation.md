# ADR 0038: Solid 2 investigation (no switch now)

Status: accepted (2026-11-24). The decision is to stay on SvelteKit 3 until the gates of section 5 hold.  
Extends: ADR 0002 (SvelteKit version), ADR 0008 (web layout), ADR 0014 (embed mode), ADR 0034 (web performance)  
Spike: branch `spike/solid2`, commit `329524c`. The branch is a reference. Do not merge it. Do not delete it.

## 1. Context

ADR 0002 selected SvelteKit 3 for the web pack. The generated web app depends on it in these places:

- The packs hold 92 `.svelte` files and 28 route files. 71 of the `.svelte` files are vendored shadcn-svelte components.
- The page generators in `internal/gen/resource/` and `internal/gen/page/` hold about 1,450 lines of Go that write Svelte.
- 708 of the 1,181 golden files and 35 of the 86 testscripts touch `web/`.

SolidJS 2.0.0 shipped on 2026-11-17. The owner asked for an investigation of a move to it.
The owner selected this scope: a spike that is never merged, a small number of seams in `main`, and this ADR.
The owner also selected the form of a later move: a hard cutover, with no `web_framework` answer and no support for two frameworks.

## 2. Findings (2026-11-24)

Each line is a fact of that date. Check it again before a cutover.

| Subject | State |
|---|---|
| `solid-js` | 2.0.0 on 2026-11-17. The latest release is 2.0.4. |
| SolidStart | It does not support Solid 2. Its replacement is start mode in `@solidjs/vite-plugin` 3.0.1. |
| Routing | `@solidjs/router` 2.0.1. The file routes come from `filesystem-routing` 0.5.0, a 0.x package. |
| Primitive libraries | Kobalte is 2.0.0-alpha.5. Ark UI for Solid 2 is on the `next` tag. No shadcn port for Solid 2 exists. |
| Forms | No superforms equivalent exists. `@formisch/solid` needs `valibot`, so it does not accept the zod schemas. |
| Tests | `@solidjs/testing-library` 1.0.0-rc.2 is on the `next` tag. |
| Icons | No Lucide package for Solid 2 exists. The spike uses inline SVG. |
| CSP | A nonce policy works. A hash policy for SSR does not: issue 4174 of `solidjs/solid` is open, and the fix is deferred until after 2.0. |
| Maintainers | About 80 percent of the recent commits come from one maintainer. |
| Pin age | `stackversions.yaml` pins the newest release that is older than 14 days. `solid-js` 2.0.4 is newer than that. pnpm 12 also refuses it without a `minimumReleaseAgeExclude` entry. |

A side finding applies to the apps of today: `zod` below 4.6.6 has an audit finding, and the pin is 4.6.5.

## 3. Spike results

The spike replaced `web/` of a generated app with a Solid 2 app written by hand.
The app has the modules `ci`, `auth`, `flags`, and `live`, and the `todo` resource of `testdata/e2e/web/run.sh`.
The acceptance was the six Playwright specs of `testdata/e2e/web/tests`, unchanged.
A second agent built each stage again from a clean tree and tried to refute each result.
`spikes/solid2/run.sh` on the branch repeats the build and the acceptance. `spikes/solid2/results.txt` holds the output and the full record.

Pins of the spike: `solid-js` 2.0.4, `@solidjs/web` 2.0.4, `@solidjs/vite-plugin` 3.0.1, `@solidjs/router` 2.0.1, `filesystem-routing` 0.5.0, `@solidjs/meta` 2.0.0, `@kobalte/core` 2.0.0-alpha.5, Vite 8.3.4, TypeScript 6.0.3, Tailwind 4.3.3.

| # | Question | Result |
|---|---|---|
| E0 | Do the pins install, and does `tsc --noEmit` replace `svelte-check`? | PASS. No peer warning. Formisch does not accept zod. |
| E1 | Does the Node server serve SSR with `/healthz` and a clean SIGTERM? | PASS, with an own `server.js` of 18 lines. The stock `dist/server/node.js` ignores SIGTERM. `BODY_SIZE_LIMIT` and `SHUTDOWN_TIMEOUT` become code that spinup owns. |
| E2 | Does a middleware replace `hooks.server.ts`? | PASS. `middleware.ts` has 58 lines. The client address rule becomes code that spinup owns. `ratelimit.spec.ts` passes. |
| E3 | Do the session cookie and the Origin check work? | PASS. The server reads `PUBLIC_ORIGIN` at run time. See caveat 3. |
| E4 | Do sign-in and forms work without JavaScript? | PASS. See caveat 4. |
| E5 | Does CRUD share the generated zod `schema.ts`? | PASS, with a form layer of 180 lines that spinup owns. `schema.ts` is byte-identical. |
| E6 | Do the menu, the dialog, and the toast pass with no CSP violation? | PASS, 16 of 16 tests. `renderMode: 'async'` is necessary for a client without JavaScript. Kobalte needed three focus workarounds. |
| E7 | Does the unchanged `live.core.ts` drive the live helpers? | PASS. `live.solid.ts` has 297 lines against 271. The 46 tests of `live.core.test.ts` pass unchanged. The verifier reproduced `liveRefresh` only. |
| E8 | Does each response carry a nonce? | PASS. The CSP header equals the SvelteKit header except for the nonce value. |
| E9 | Does the SPA build serve through `app.WithSPA`? | PASS, with the kit change of seam B4. The files are in `/assets/` with content hashes. `index.html` has no inline script. |
| E10 | Do sign-in and CRUD work in the SPA with direct calls to the Go API? | PARTIAL. 10 of 16 tests pass against the one binary. See below. |
| E11 | Does server tracing reach the Go API? | PASS. `start.serverSetup` loads the OTel SDK, so `node` needs no `--import` flag. The Docker trace e2e was not run. |
| E12 | How do the bundle and the SSR server compare? | The bundle is in budget. The SSR server is about 2 times slower. See below. |
| E13 | How does an agent write Solid 2 code? | Each first draft had 3 or 4 silent Solid 1.x habits that `eslint` and `tsc` did not find. |

### E10: SPA mode

The same pages serve SSR and the SPA. A Vite alias replaces three modules with browser twins of about 139 lines.
The Go `CrossOriginProtection` already protects the JSON calls that carry the cookie.
The 6 failed tests cannot pass in an SPA, or they wait for a server-function URL:

- 2 tests wait for a POST whose URL contains `toggle`. The SPA sends `PUT /api/todos/{id}`.
- 1 test expects status 404 for the document. The shell always gives 200.
- 1 test posts a form without JavaScript.
- 2 tests need a script nonce in the CSP.

So the embed mode refusals of `docs/embed.md` (the `auth` module, server pages) can go at a cutover.

### E12: measurements

Machine: Apple M1 Max, load average 8 to 20 during the runs. Three interleaved rounds. SvelteKit is the generated app of the same commit.

| Metric | Solid 2 | SvelteKit 3 | Factor |
|---|---|---|---|
| SSR `/todos`, requests each second | 266 | 514 | 0.52 |
| SSR `/login`, requests each second | 1,550 | 3,172 | 0.49 |
| Latency p50 at 32 connections | 116.5 ms | 59.6 ms | 1.95 |
| TTFB p50 | 7.7 ms | 5.15 ms | 1.50 |
| Start time | 477 ms | 347 ms | 1.37 |
| Idle RSS | 104.5 MiB | 80.0 MiB | 1.31 |
| Client bundle, raw bytes | 582,518 | 611,706 | 0.95 |
| Client bundle, brotli bytes | 168,424 | 167,679 | 1.00 |
| First load of `/login`, brotli bytes | 136,924 | 101,949 | 1.34 |
| First load of `/todos`, brotli bytes | 143,501 | 131,030 | 1.10 |
| Cold build | 2.34 s | 5.5 s | 0.43 |
| Lines that spinup owns, without vendored UI | 2,578 | 2,262 | 1.14 |
| Lines with vendored UI | 3,073 | 4,115 | 0.75 |

The bundle is inside `perf/budgets.json` (666,000 raw bytes, 183,000 brotli bytes).
The ranges of the request rates did not overlap in any round. Nobody profiled the SSR path, so the cause is unknown.
The Solid app has no `/account` page and no `/admin/flags` page. With those pages, the line totals are about equal.

### Caveats

1. **SSR speed.** The Solid server gives about half the request rate. A cutover must profile this first.
2. **Build output.** The Solid client build is not reproducible. Two builds of one tree gave different names for 14 of 30 chunks. SvelteKit gave 0 of 41. The cause is unknown.
3. **Origin check.** Only the server-function endpoints refuse a cross-origin POST. A cross-origin POST to a page route is not refused. SvelteKit refuses it. No page route changes state in the spike.
4. **Flash cookie.** The error display without JavaScript uses a cookie with the `Secure` attribute. It works on `127.0.0.1`. Plain HTTP on another host is not tested and is probably broken.
5. **Shutdown.** In one run the Solid server did not exit on SIGTERM while a browser held a keep-alive connection. Nobody isolated it.
6. **Build order.** `build:spa` after `build` deletes `dist/server`.
7. **Embed CSP.** The CSP of the embed shell has no `form-action` and no `img-src data:` directive. This is the kit policy of today, not a result of Solid.
8. **Public files.** `app.WithSPA` serves each regular file, also files in a directory whose name starts with a dot. Vite leaves `.vite/manifest.json` in the output. The spike deletes it in a build plugin.
9. **Agents.** The skill files that `solid-js` ships cover the reactive primitives. They do not cover the router and the form layer. The most dangerous silent mistake is a write that the framework holds until the next flush.

## 4. Fit analysis

**Neutral parts.** These files moved to the Solid app unchanged or almost unchanged: `lib/api/client.ts`, `schema.d.ts`, each zod `schema.ts`, `live.core.ts` with its test, `app.css`, and the logic of `lib/server/*`.

**Parts to write again at a cutover.**

- The 14 UI components that the app uses, on a primitive library for Solid 2.
- `hooks.server.ts`, which becomes a middleware, an own server entry, and a server configuration file.
- The module pages of the packs `auth`, `flags`, `audit`, `apikeys`, and `billing`.
- The generators `pages_svelte.go`, `pages_server.go`, `pages_fields.go`, `history_pages.go`, `gen/page/page.go`, and `gen/component/component.go`.
- `live.svelte.ts`, both `vite.config.js` files, `Dockerfile.web.tmpl`, and the Compose environment of the web service.
- A form layer that replaces superforms (180 lines in the spike).
- The agent guidance of ADR 0035, with rules for the router and the form layer.

**New code that spinup owns.** SvelteKit and adapter-node give these as configuration. In Solid 2 they are code in the app: the SIGTERM drain, the body size limit, the client address rule, the CSP builder with its nonce, and the rewrite of a redirect `Location` behind TLS.

**Possible gains.**

- `PUBLIC_ORIGIN` at run time. One image can serve more than one origin. ADR 0002 rule 2 makes the origin a build input.
- Embed mode with the `auth` module and resource pages, from the same page files.
- A server span for each page request. SvelteKit gives only the client spans of `undici`.
- Fewer lines of vendored UI (495 against 1,853), and a cold build that is about 2 times faster.

## 5. Readiness gates

Do not start the cutover before all gates hold. Each gate has a check.

| # | Gate | Check |
|---|---|---|
| 1 | A stable primitive library for Solid 2 covers menu, dialog, and sheet. | `npm view @kobalte/core dist-tags`, `npm view @ark-ui/solid dist-tags` |
| 2 | `@solidjs/testing-library` is at 1.0.0 or later on `latest`. | `npm view @solidjs/testing-library dist-tags` |
| 3 | `filesystem-routing` is at 1.0 or later, or start mode has a published support statement. | `npm view filesystem-routing version`, the Solid documentation |
| 4 | 90 days pass with no 2.0.x release that fixes a regression in `Show`, `For`, or async. The clock starts at the release date of 2.0.4. A later fix of that kind starts the clock again. | The changelog of `solid-js` |
| 5 | Each pin is older than 14 days. | `npm view <package> time` |
| 6 | `spikes/solid2/run.sh` passes on the new pins: 16 of 16 tests on SSR. | Run the script on the branch. |
| 7 | The bundle is inside `perf/budgets.json`, and the SSR request rate of `/todos` is at least 0.8 times the SvelteKit rate on the same machine. | `bundle.py --assets assets`, `scripts/perf/loadgen` |
| 8 | Two builds of one tree give the same file names. | Build two times and compare `dist/client/assets`. |

Today only gate 6 holds. Gate 7 holds for the bundle and fails for the request rate.
The approved plan named gates 1 to 6 and the bundle part of gate 7. The request rate of gate 7 and gate 8 come from the spike results.

## 6. Hard-cutover design

This is the design for the cutover. It is not work for now.

1. One template version is the boundary. The versions before it render SvelteKit. The versions from it render Solid. No `web_framework` answer exists.
2. `spinup upgrade` cannot convert an app. `internal/upgrade/plan.go` keeps each generator-owned file, so the engine never writes a page again.
3. `0001_web_svmigrate` returns nil when the new stack has no `@sveltejs/kit` pin. Today it fails for that stack.
4. A new `Before` codemod stops the upgrade while `web/package.json` holds `@sveltejs/kit`. Its `Try:` line names the guide.
5. The generator-owned and scaffold `.svelte` files stay on disk. The owner ports them by hand. The guide is `docs/upgrading-solid.md`. It covers `todo` and `jostar`.
6. The `shadcn_registry` question goes. `upgrade --ui` merges seed components that spinup owns. `g component` goes or changes.
7. `DocsBase` in `internal/render/fragments.go` points at `main`. The last Svelte template version must render `docurl` links that are pinned to a tag. Do this before the documents on `main` change.
8. A plugin pack cannot hold a second web framework. Plugin authors port their web files at the same boundary.
9. The asset directory of the web build changes from `_app/immutable` to `assets`. Seams B2 and B4 make it a parameter of `bundle.py` and of the kit. The build directory changes from `web/build` to `web/dist`. Seam B1 gives it one owner.
10. The Vite build of embed mode deletes `.vite/`, or `app.WithSPA` skips paths that start with a dot.

Open questions for the cutover:

- Does the stopping codemod also move `web/` to `web.svelte/` and render a new `web/`?
- Does the page-route Origin gap (caveat 3) need a check in the middleware?
- Which primitive library replaces shadcn-svelte, and who owns its components after `upgrade --ui`?

## 7. Seams merged to `main`

Each seam removes a SvelteKit literal. No seam adds a framework choice. No seam changes rendered output.

| Seam | Change |
|---|---|
| B1 | `webtool.BuildDir` and `webtool.ClientDir` own the paths `web/build` and `build/client`. `internal/build` and the `web-bundle` doctor check use them. |
| B2 | `scripts/perf/bundle.py` has the option `--assets <rel>`. The default is `_app/immutable`. |
| B3 | `TestNoRawHTML` uses a pattern table, with rows for `{@html`, `innerHTML=`, and `dangerouslySetInnerHTML`. |
| B4 | `app.WithSPAOptions(files, SPAOptions{AssetDir, ImmutablePrefix})` in the kit. `app.WithSPA` calls it with `_app/` and `_app/immutable/`. No template calls the new option. |

Rejected seams: the dev port and dev flags (both frameworks use Vite), a skip in `0001_web_svmigrate` (it removes a useful guard today), one detection function in `nav.go` (the route layout was unknown), and parameters for `suite_web.py` (a correct general form needs a second user).

## 8. Not done

- No switch. No support for two frameworks.
- No change in a template pack, in `stackversions.yaml`, or in a generator.
- No profile of the Solid SSR path.
- No run on Firefox or WebKit, no Docker image, no deploy, and no run behind Caddy with TLS.
- No Solid twin of the `/account` and `/admin/flags` pages.
