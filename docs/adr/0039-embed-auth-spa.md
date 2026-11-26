# ADR 0039: embed mode with sign-in and resource pages (spike)

Status: accepted (2026-11-25). The owner confirmed the ruling of section 6 on 2026-11-25. Stage 0 of section 7 is done. Stages 1 to 3 are in work under section 10. Stage 4 remains approved for later work.
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

Go on feasibility. The spike run implemented nothing.
On 2026-11-25 the owner confirmed the ruling and approved the work, on the condition that each stage is an improvement. Stage 0 followed on the same day.
Embed mode can hold the `auth` module and resource pages on SvelteKit 3. The refusals of ADR 0014 can go in a later milestone.
Stage 0 does not remove them. Until stage 2 and stage 3 merge, the refusals stay as they are.

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

**Stage 0, the kit and the embed pack of today (done on 2026-11-25).**
- The shell policy in `kit/app/spa_index.go` has the directives of node mode (ADR 0036, section 2, item 1), with the script hashes in place of the nonce. It adds `form-action 'self'`, `img-src 'self' data:`, `font-src 'self' data:`, and `connect-src 'self'`.
- The embed `vite.config.js` sets `version.name` to a hash of the build inputs: `src/`, `static/`, `package.json`, the lockfile, `vite.config.js`, and each `PUBLIC_` value. Before, the build time in `index.html` changed the script hash for each build.
- Node mode keeps the build time as the version name. Its policy uses a nonce, so the script hash is not a concern there. Not changed.

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

## 8. Decisions for this run

- The first release adds auth, flags, live data, and resource pages to embed mode. Section 10 brings the flags page forward from stage 4.
- The release keeps separate refusals for apikeys, billing, audit, and mcp.
- The Go API keeps the 401 for a signed-out session read. The browser reports that response in its console.
- A cookie-clear route is declined for now. A failed sign-out shows an error and leaves the user signed in.
- Sign-out stops every local live stream before it sends the request. Completion depends on events. There is no grace period.
- The root reads public flags at bootstrap, on a session refresh, and after a successful admin write. Ordinary navigation reuses that result.
- This run requires no changes in `kit/`.
- Embed mode gets its own bundle budget, `perf/budgets-embed.json` (section 10.9). The node budget does not change.

## 9. Not done

- Firefox and WebKit.
- A Docker image, a deploy, and Caddy with TLS.
- A real tab switch. The probe dispatched `visibilitychange`, because headless Chromium does not fire it.
- First paint time.
- The reset pages, OIDC, the history pages, apikeys, billing, and mcp.
- Browser traces.
- A signed-in node page measurement. The node CSP came from a build with no API behind it.
- A stable version name in node mode. Stage 0 changed embed mode only.

## 10. Contracts of stage 1 to stage 3

These contracts govern the implementation. Sections 2 to 5 and 9 describe the historical spike. Section 8 records the release decisions.

### 10.1 Pack selection and file ownership

All pack paths below start at `internal/templates/packs/`. All paths in the output column start at the generated project root.
Braces enumerate files. Expand each brace once per listed name. Square brackets in SvelteKit route segments are literal.
The table specifies the **target** tree. New source paths are deliverables. Existing files and spike references provide the evidence.

Use these exact conditions in pack metadata. An omitted metadata condition means unconditional selection inside the selected pack.

| Key | `when` |
| --- | --- |
| E | `frontend_mode == 'embed'` |
| N | `frontend_mode != 'embed'` |
| EA | `frontend_mode == 'embed' && modules contains 'auth'` |
| NA | `frontend_mode != 'embed' && modules contains 'auth'` |
| EM | `frontend_mode == 'embed' && modules contains 'mail'` in the auth pack |
| NM | `frontend_mode != 'embed' && modules contains 'mail'` in the auth pack |

The loader rejects duplicate literal metadata keys at `internal/templates/loader.go:202`.
For an alternate output, use the expression `'[[ "web/src/routes/login/+page.svelte" ]]'` with the actual output path inside the quotes.
Keep the primary entry literal. Keep the existing expression for each embed root layout.
The renderer tests the condition before path expansion. Exactly one selected entry may produce a given path. See `internal/render/render.go:46` and `:57`.
The grammar supports conjunctions and positive membership. It has no negative membership operator. See `internal/templates/loader_when.go:48`.
Use the template branch for the no-auth layouts. It preserves their current bytes, including the final newline.

For the following table, `L` means `web/src/lib/` and `R` means `web/src/routes/` in the output column.
A trailing `.tmpl` belongs to the source. All other sources are literal files.
Separate metadata fragments allow a unit to ship the declaration with its source.

| Pack source | Output | Class | When | Answers used inside the source |
| --- | --- | --- | --- | --- |
| `web/embed/vite.config.js` | `web/vite.config.js` | managed | E | none |
| `web/embed/layout.ts.tmpl` replaces `web/embed/layout.ts` | R`+layout.ts` | scaffold | E | modules, for auth |
| `web/embed/layout.svelte.tmpl` | R`+layout.svelte` | scaffold | E | app_name, modules for auth and flags |
| `web/embed/routes/+page.ts` | R`+page.ts` | scaffold | EA | none |
| `web/embed/components/app/user-menu.svelte` | L`components/app/user-menu.svelte` | scaffold | EA | none. Accept optional links in all auth builds |
| `web/embed/lib/{auth.ts,session-watch.ts,sign-out.ts}` | L`{auth.ts,session-watch.ts,sign-out.ts}` | managed | EA | none |
| `web/embed/lib/session.svelte.ts.tmpl` | L`session.svelte.ts` | managed | EA | modules, for flags |
| `web/embed/lib/{auth.test.ts,session-watch.test.ts,sign-out.test.ts}` | L`{auth.test.ts,session-watch.test.ts,sign-out.test.ts}` | managed | EA | none |
| `auth/embed/lib/auth-form.ts` | L`auth-form.ts` | managed | E | none |
| `auth/embed/routes/{login,register,account}/+page.ts` | R`{login,register,account}/+page.ts` | scaffold | E | none |
| `auth/embed/routes/login/+page.svelte.tmpl` | R`login/+page.svelte` | scaffold | E | auth_oidc, modules for mail |
| `auth/embed/routes/{register,account}/+page.svelte` | R`{register,account}/+page.svelte` | scaffold | E | none |
| `auth/embed/routes/login/schema.ts` | R`login/schema.ts` | scaffold | E | none |
| `auth/embed/routes/{reset,reset/[token]}/{+page.ts,+page.svelte,schema.ts}` | R`{reset,reset/[token]}/{+page.ts,+page.svelte,schema.ts}` | scaffold | EM | none |
| `flags/embed/lib/flags-admin.ts` | L`flags-admin.ts` | managed | E | none |
| `flags/embed/routes/admin/flags/{+page.ts,+page.svelte}` | R`admin/flags/{+page.ts,+page.svelte}` | scaffold | E | none |
| `live/embed/lib/{live.svelte.ts,live-session.test.ts}` | L`{live.svelte.ts,live-session.test.ts}` | managed | E | none |

- Declare the web helpers in `web/pack.d/embed-session.toml`. Declare the layouts, home load, and menu in `web/pack.d/embed.toml`. Change the menu condition in `web/pack.d/shell.toml` to NA.
- The existing node layout, home load, hooks, tracing, health route, and Vite config already have mode conditions. Keep the common root home component and error page.
- In `auth/pack.toml`, apply N to L`server/{auth.ts,auth.test.ts}`, R`{login,register,account}/{+page.server.ts,+page.svelte}`, and R`logout/+page.server.ts`.
  Declare the browser twins in `auth/pack.d/embed.toml`. The embed menu calls the API. It needs no logout page.
- In `auth/pack.d/reset.toml`, apply NM to R`{reset,reset/[token]}/{+page.server.ts,+page.svelte}`. Declare the EM entries there.
  Keep the API migrations, reset mail, and Go routes under their existing conditions.
  The managed reset Go template reads `module` and additionally `frontend_mode` for the new middleware. Keep L`components/OidcButtons.svelte` under `auth_oidc` in both modes.
- In `flags/pack.toml`, apply N to L`server/flags.ts` and R`admin/flags/{+page.server.ts,+page.svelte}`. Declare its embed entries there.
  Reuse the existing admin `schema.ts`, `lib/flags.ts`, and `lib/flags.gen.ts`. Put `FLAGS_ADMIN_DEP = 'app:flags-admin'` in the browser helper.
- In `live/pack.toml`, apply N to L`live.svelte.ts` and declare its embed entries. Share `live.core.ts` and `live.core.test.ts`.
- Copy the node `zodJitless` transform into both embed plugin lists. See `web/files/web/vite.config.js:15`, `:37`, and `:80`.
  Apply this to the embed config, including apps without auth. Preserve the stage 0 build hash.
  For a fresh embed app without auth, this config is the sole rendered difference. A later generator invocation can add resource pages.
- Preserve all existing node render bytes, including agent guidance. Test the conditional whitespace. Each managed source has a lock entry.

### 10.2 Session, navigation, and live data

The following TypeScript declarations specify the complete exports of the new session modules.
`F` below denotes `typeof globalThis.fetch`. It is a notation for the signatures, not an exported type.

```ts
// lib/auth.ts
export const SESSION_DEP = 'app:session'
export const FLAGS_DEP = 'app:flags'
export interface AuthErrors { email?: string; password?: string; form?: string }
export type AuthResult = { ok: true } | { ok: false; status: number; errors: AuthErrors }
export function safeRedirect(target: string | null, fallback: string): string
export function loginHref(url: URL): string
export function requireUser(user: App.User | null, url: URL): App.User
export function requireRole(user: App.User | null, url: URL, ...roles: string[]): App.User
export function requireGuest(user: App.User | null, url: URL): string
export function postAuth(fetch: F, path: `/api/${string}`, body?: Record<string, string>): Promise<AuthResult>
export function loadSession(fetch: F): Promise<App.User | null>
export function loadFlags(fetch: F): Promise<Record<string, boolean>>

// lib/session.svelte.ts
export type SessionData = { user: App.User | null; flags: Record<string, boolean> }
export const session: {
  readonly data: SessionData
  readonly signingOut: boolean
  readonly changingUser: boolean
  readonly generation: number
}
export function readSession(fetch: F): Promise<SessionData>
export function startSignOut(): void
export function cancelSignOut(): void
export function leaveSession(target: string): void
export function onSessionPause(stop: () => void, resume: () => void): () => void

// lib/session-watch.ts
export function watchSession(): () => void
export function recheckSession(): void
export function flushSessionCheck(): void
export function observedFetch(fetch: F): F
export function announceSessionChange(): void

// lib/sign-out.ts
export function signOut(): Promise<boolean>

// routes/+layout.ts (auth build)
export const ssr = false
export const load: LayoutLoad
```

`session.svelte.ts` owns the in-memory user, flags, transition state, and generation. Expose read-only Svelte state.
Layout data is a projection. There is no persisted user, token, flag map, second writable store, or cookie access in JavaScript.
The Go session response has `{user:{id,email,role}}`. Only its 401 means signed out. A session transport, protocol, or server error fails the load closed.
Hide protected content on verification failure. Show a retryable error. Do not broadcast a sign-out on failure.
Read public flags only when selected. On a flags failure, warn and publish an empty map. The API evaluates all rules.

1. The authenticated root imports the type `LayoutLoad` from `./$types`. It calls `depends(SESSION_DEP, FLAGS_DEP)` and returns `await readSession(fetch)`. Read both endpoints concurrently with the load's fetch and same-origin credentials.
   Without flags, resolve the map to `{}` locally. Root load never reads the URL and never issues a guard redirect.
   Without auth, generate just today's `ssr = false` layout. The SPA bootstrap must also work with no resource and home equal to `/`.
2. In each protected load, await `parent()`, then run the guard before any protected fetch. Only the leaf guard redirects on first access.
   `requireUser` returns the user or redirects with 303 to `loginHref(url)`. `requireRole` calls it first, then uses membership in the supplied roles.
   Use the exact 403 text from `auth/files/web/src/lib/server/auth.ts:14`.
   Copy `safeRedirect` and its hostile-input cases from that file and its test. Check raw and decoded paths.
   `loginHref` encodes pathname plus search once. On login and register it returns `/login` with the existing search only when `redirectTo` exists.
   Use `spikes/embed-auth/web/src/lib/auth.ts:25` on the spike branch without the clock branch. Node inlines the basic redirect in `requireUser`.
   `requireGuest` redirects a signed-in visitor to `home` with 303. Otherwise it returns `safeRedirect(url.searchParams.get('redirectTo'), home)`.
   The home load redirects a signed-in user only when `home !== '/'`.
3. Open the channel before the first client session fetch. Buffer change messages until bootstrap completes.
   Mount the watch once in the root. Adopt that channel and drain buffered work. Remove listeners and close the channel on teardown.
   Use channel `spinup:session` and message `{type:'session-changed'}`. Accept only that message. It carries no identity.
   On receipt, and whenever the document becomes visible, request a session check. Revalidate a restored back-forward cache entry on `pageshow` with `persisted`.
   Never trust an identity from another tab. Successful password sign-in, registration, sign-out, and reset confirmation announce the change.
   For OIDC, set sessionStorage key `spinup:oidc-pending` to `1` on the provider link click. Consume the marker on return after the first session read.
   Announce a successful return with a user. Clear the marker on failure. Store no return credentials.
4. All generated authenticated API consumers observe 401 responses. Use `observedFetch(fetch)` for resource calls, flag administration and form submissions.
   The wrapper passes through the response and requests a check for a same-origin `/api/` 401 in the current generation.
   Exclude the bootstrap reads and the explicit logout transaction. Route live stream and snapshot 401s to the same check.
   This works with both generated clients. The heyapi wrapper implements the request methods but has no `api.use` (`web/variants/api.ts`).
   Initial loads can queue a check before the component mounts. An HTTP failure during navigation must still deliver the check on the resulting page.
5. Coalesce events into one pending read and retain a dirty bit for an event during a read. After navigation settles, drain pending work.
   Register `onNavigate` in the root. Chain `flushSessionCheck` to its `complete` promise without blocking navigation. Use completion signals, without a timed retry.
   Retain the last accepted identity through a failed read. Hide its children until verification succeeds.
   On a superseded read, await the winning snapshot. During document replacement, terminate the old load without returning data to its guards.
   The watch invalidates `SESSION_DEP`. The root rereads session and flags. A flags save invalidates `FLAGS_DEP` and `FLAGS_ADMIN_DEP` together.
   Never invalidate on every page change. Use a generation check before publishing any asynchronous result.
6. After bootstrap, compare the verified ID, role, and email to the previous tuple, including null.
   On change outside a local auth transaction, hide the old shell and children immediately. Stop live work and clear flags and user data.
   Replace the document with plain `/login` for a lost user and `home` for a new or changed user.
   Use `window.location.replace(target)` for identity changes. A full document replacement discards page data, drafts, optimistic writes, snapshots, preloads, and pending callbacks from the previous identity.
   Reject completions from the older generation. A new root reads the cookie again.
   On local sign-in, use the sanitized return path instead. First bootstrap never overrides an OIDC return path.
7. `signOut()` is single-flight. Set `signingOut`, advance the generation, and suppress queued watcher invalidations synchronously.
   Invoke every registered stop callback before the first `await`. A subscription arriving while paused must start paused.
   Abort shared streams and snapshots. Cancel refresh, reconnect, and debounce callbacks. Prevent visibility and online handlers from reopening a stream.
   Register both `liveRefresh` and `liveList` through `onSessionPause`. Keep ADR 0033's public API and one shared stream per feed.
   Before calling `recheckSession()`, compare the captured generation with the current generation and check the pause. They never call `invalidateAll()` for an unauthorized response.
   Only after all stops return, send `POST /api/auth/logout`. On 204 announce the change and call `leaveSession('/login')`.
   This clears local state and replaces the document. Keep the pause active through departure. No old guard can win a client navigation.
   On failure keep the session and URL, release the pause, and recreate memberships under the new generation. Deliver any pending external change after recovery.
   Return false and display `Sign-out failed. ` followed by the mapped form error, with `Try again.` as fallback. Permit another attempt.
   A failed API call cannot clear the HttpOnly cookie. There is no timer in this protocol.
8. A signed-out document load produces one browser console line for `GET /api/session` returning 401. Visibility and explicit checks can cause further reads.
   Record this behavior in `docs/embed.md`. Do not log a second application error for an expected 401.

The spike supplies the form, guard, and live examples under `spikes/embed-auth/web/src/`. Its `NOTES.md` documents the double redirect and stale tabs.
The node stream and snapshot 401 paths are in `live/files/web/src/lib/live.svelte.ts:56` and `:212`.

### 10.3 Accounts and flags

Use same-origin JSON requests. Let the browser handle the session cookie.
Use `postAuth` for credential forms. Its error type is `{email?: string; password?: string; form?: string}`.
Its result is `{ok:true} | {ok:false; status:number; errors:AuthErrors}`.
Keep field messages for known inputs. Show the first message per field. Show the problem detail as a form error when no input matches.
Preserve the email and unsubmitted edits, including the password in the mounted form. Never put a password in load data, logs, URLs, or storage.
Map a transport exception to 503 and `The server is not available. Try again later.`
Map a server error other than 503 to `Something went wrong on the server. Try again later.`
Preserve a busy 503 detail. Fall back to `The server is busy. Try again in a moment.`
Use `The request failed (<status>).` for a problem without detail. Render text through Svelte escaping.

| Page | Load and schema | API, success, and errors |
| --- | --- | --- |
| `/login` | Guest guard. Return `redirectTo`. Use `login/schema.ts:loginSchema`. | POST `/api/auth/login`, JSON email and password. On 200 with user, announce and replace the document at the sanitized target. A credential 401 shows `The email address or password is incorrect.` on this page. |
| `/register` | Same guard and return link. Import `registerSchema` from `../login/schema`. | POST `/api/auth/register`, JSON email and password. On 201 with user, follow the same success path. Show the 409 email conflict at the email field. Show 422 field errors and the 403 registration detail. |
| `/account` | Return `{user:requireUser(user,url)}` from the parent result. No schema. | Display the session email. The button and the user menu both call `signOut()`. Logout sends no JSON body. |
| `/reset` | Public load returns `{}`. Local `resetRequestSchema` requires a trimmed email. | POST `/api/auth/reset/request` with `{email}`. On 202 stay here and show `If an account exists for this email address, you will receive a reset link.` |
| `/reset/[token]` | Public load supplies `params.token`. Local `resetConfirmSchema` validates the new password. | POST `/api/auth/reset/confirm` with `{token,password}`. On 204 announce and replace with `/login`. Invalid or expired tokens give a 422 form detail. Password errors belong at the password field. |

The paths and responses come from `kit/auth/handlers.go:46`, `:101`, `:190`, `:208`, `:222`, `:231`, and `kit/auth/registration.go:48`.
The reset handlers are in `kit/auth/reset.go:87`, `:113`, `:179`, and `:214`. Keep their non-disclosing answers.
For ADR 0016's network limiter, show the API detail unchanged, including the singular or plural retry interval. Preserve its 429 and `Retry-After`.
The per-email login lockout has its own 429 detail. Do not invent a countdown for it. See `kit/auth/ratelimit.go:625` and `kit/auth/handlers.go:154`.

- Use the spike's `authForm(schema: AuthSchema, path: '/api/auth/login' | '/api/auth/register', redirectTo: () => string): SuperForm<{email:string; password:string}>`.
  The helper exports only `authForm`. Its private `AuthSchema` is the spike's zod object with string email and password.
  Use `superForm`, `defaults(zod4(schema))`, `SPA:true`, the endpoint as form ID, and `resetForm:false`. Show form messages at `data-testid="form-error"`.
  Use required email inputs. Require a nonempty login password. Require 12 to 512 characters for registration and confirmation. Keep the 512-character login input limit.
  Preserve API validation for Unicode passwords. Measure password length in Unicode code points when enforcing Go's limits.
- The mail answer adds the reset link on login. OIDC adds the existing buttons on login.
  Give the buttons the sanitized `next`. Fetch `GET /api/auth/oidc/providers` for the 200 array of names and labels.
  Use ordinary document links to `/api/auth/oidc/{provider}/start?next=<encoded-path>`. Encode the provider segment.
  Capture the provider click for the pending-flow marker in the embed login component. Keep the shared component byte-identical.
  The start and successful callback both redirect with 303. Go validates state and the return path, signs in, and redirects to the stored path.
  The callback is `GET /api/auth/oidc/{provider}/callback`. A limited callback redirects to `/login?error=rate_limited`.
  Show `Too many sign-in attempts. Try again later.` there. See `kit/auth/oidc.go:118`, `:297`, `:401`, and `kit/auth/ratelimit.go:632`.
- Preserve token privacy on the first document response, including its first asset request.
  The node token load sets `Referrer-Policy: no-referrer` and `Cache-Control: no-store` in `auth/files/web/src/routes/reset/[token]/+page.server.ts:5`.
  For embed with auth and mail, add `resetPageHeaders` to `auth/files/api/cmd/api/reset.go.tmpl` and wire `app.WithMiddleware(resetPageHeaders)` in `api/files/api/cmd/api/main.go.tmpl`.
  Match GET and HEAD for `/reset/<single-segment>`. Set both headers at response commit, including implicit writes and 304 responses.
  The SPA handler sets its own cache header at `kit/app/spa.go:118`. Preserve its CSP, compression, ETag, and status.
  Keep the reset referrer meta on both Svelte pages. Do not change `web/files/web/src/app.html.tmpl`. Each request of the shell goes to the same origin, and the Go middleware covers the document response.
  Keep all node outputs and no-auth outputs byte-identical.
- The admin flag load calls `requireRole(user, url, adminRole)` before reading the admin endpoint. Pass the imported role from `#lib/flags.gen.js`.
  Show the menu link only for that role. Copy the names, signatures, types, and relative paths from the spike's `lib/flags-admin.ts`.
  Use `listFlags(fetch)`, `putRule(fetch,key,rule)`, `putUser(fetch,key,email,enabled)`, and `clearUser(fetch,key,email)`.
  Keep `ruleSchema`, `userSchema`, and `clearSchema` from the node schema. Show API errors at the corresponding form.
  GET returns the admin data. The two PUT operations return the updated entry. DELETE returns 204. Encode key and email segments.
  Keep the Go role checks on every call. See ADR 0029 sections 8 and 10. After a save, refresh both admin and public data in one invalidation.
  Clear pending UI state on all outcomes.

### 10.4 Module and generator boundaries

Replace the auth checks at `internal/gen/newapp/newapp.go:150` and `internal/modules/select.go:66` with the same policy.
Share the pure check in the newapp package, which select already imports. Check explicitly requested names before dependencies so a request for mcp names mcp.
At creation, retain the missing-dependency error for `--modules live` alone. At addition, resolve required packs as today.
Apply the check to resolved dependencies too. Keep the existing API-only checks. Module failures occur before writes, branch creation, and upgrade state.

The following table gives complete CLI errors. Join the two columns with a newline.

| First line | Last line |
| --- | --- |
| `cannot use apikeys in embed mode: the API key pages require a SvelteKit server` | `Try: use --frontend-mode node for apikeys` |
| `cannot use billing in embed mode: the billing pages require a SvelteKit server` | `Try: use --frontend-mode node for billing` |
| `cannot use audit in embed mode: the history pages require a SvelteKit server` | `Try: use --frontend-mode node for audit` |
| `cannot use mcp in embed mode: the module requires apikeys` | `Try: use --frontend-mode node for mcp` |

- Auth, flags, and live pass. Base, api, web, ci, deploy, console, jobs, mail, monitoring, backups, secrets, and tofu keep their existing requirements.
  None needs another SvelteKit server page. Mail enables the auth reset family. Console provides an independent operator service.
  CI already starts the tagged Go binary for embed smoke tests. Monitoring's web-server tracing already selects node mode.
  The evidence is each named pack's `pack.toml`, `web/pack.d/tracing.toml`, and `web/files/.github/workflows/e2e.yml.tmpl`. The internal example pack is `_sample`.
- `spinup g audit` and `spinup g resource --audit` cannot attach audit in a supported embed app, including with `--api-only`.
  Refuse explicit audit requests there with the audit error above, before the generic advice to add audit.
  Cover the entry points in `internal/gen/audit/audit.go` and `internal/gen/resource/audit.go`.
  Keep the lower-level check for a legacy or hand-edited lock that already contains audit.
  For an explicit request with audit already present and history pages enabled, return the history-page error.
  Its exact history-page error is `cannot generate history pages in embed mode: the history page requires a SvelteKit server` followed by `Try: use an app with --frontend-mode node`.
  A refusal returns no partial plan. See `internal/gen/resource/history_pages.go:21`.
- For module addition, inspect the **locked** auth pack for the embed login load source specified in section 10.1.
  If an old template archive lacks it, fail before preview with `cannot add auth in embed mode: the locked templates lack the browser auth pages` followed by `Try: run spinup upgrade, then repeat the module addition`.
  This applies to transitive additions. A new CLI must never render an old node login into an embed app.

### 10.5 Resource pages and standalone pages

Select the page family from the lock in `internal/gen/resource/pages.go:91`. Keep the existing node table and templates intact.
Use the existing page data, path inflection, component checks, registered part, and client post-step.
Under `web/src/routes/<plural-kebab>/`, write the following eleven scaffolds.

| Output | Implementation contract |
| --- | --- |
| `{+page.ts,+page.svelte}` | List and paginate. The list owns the row menu, toggle state, and delete dialog. |
| `new/{+page.ts,+page.svelte}` | Guard before showing inputs. POST the validated body to the collection. Navigate to the created item. |
| `[id]/{+page.ts,+page.svelte}` | Load one item. Show the read view, edit link, and delete dialog. |
| `[id]/edit/{+page.ts,+page.svelte}` | Load the item. Initialize from its values. PUT the full body and return to the item. |
| `fields.svelte`, `schema.ts` | Reuse `fieldsSvelte` from `pages_svelte.go` and `schemaTS` from `pages_schema.go`. Keep all field conversions. |
| `form.ts` | Share the dependency, ID parsing, typed item fetch, unreachable text, and problem mapping. |

Place the new Go template constants in `internal/gen/resource/pages_embed_load.go` and `internal/gen/resource/pages_embed_svelte.go`.
Name the constants `embedListTS`, `embedNewTS`, `embedItemTS`, `embedEditTS`, `embedFormTS`, `embedListSvelte`, `embedNewSvelte`, `embedItemSvelte`, and `embedEditSvelte`.
The source reference is the spike's `web/src/routes/todos/`. Keep its accessible labels, heading levels, menu behavior, and one-shot feedback.

- Export `RESOURCE_DEP = 'app:resource:<plural-kebab>'` in each `form.ts`. A namespaced key prevents collisions with `app:session` and `app:flags`.
  Export the spike's `id`, `getItem`, `notFound`, `unreachable`, and `applyFailure` under those names. Use the same resource key for loads and writes.
  Use `id(value: string | null): number`, `getItem(fetch: F, param: string): Promise<Item>`, and `applyFailure(form: SuperValidated<Values>, problem: Problem | undefined, status: number): void`.
  Both messages are strings. The not-found text is `This <resource in words> does not exist.`
- Omit all auth imports for no-auth resources. For auth, await the parent and call `requireUser` in each load.
  With `--role`, use `requireRole` with the declared roles instead. The Go API guards every operation, including direct writes.
  Default ownership and `--shared` follow ADR 0015. Never send an owner ID from the form.
  An invalid ID and another owner's ID both show the resource's 404 view. The Go endpoint returns 404 for the other owner's row.
- Use the browser's typed `api.GET/POST/PUT/DELETE` on the existing API paths. Supply the observed load fetch and observed mutation fetch when auth is present.
  Both API clients support the fetch option. Keep the original time value when the field converter needs it.
  Use superforms in SPA mode for create and edit. Preserve field errors, form errors, dirty values, and post-success navigation. Escape all rendered text.
- Read 20 rows with `limit=20` and `offset=(page-1)*20`. A missing, nonpositive, or invalid safe integer selects page one.
  Redirect past the end to the last page, with a minimum of one. Invalidate the current list after writes to retain its query and page.
  For `--live`, call `liveRefresh('<plural_snake>', { invalidate: RESOURCE_DEP })` on the list. Use this example in embed advice from `spinup g live`.
- Toggle only the first non-nullable bool. Suppress concurrent clicks for that row. GET the current item.
  Send a full PUT to `/api/<collection>/{id}` with the changed bool and the latest converted values. Restore the checkbox on failure.
  Confirm delete in the alert dialog before DELETE. Cancel sends nothing. Treat a successful delete and an already missing row as complete.
  Refresh the list in place. From the item view return to the list. Show the success toast once.
- Catch transport exceptions on reads and every mutation. Show `The server did not answer. Check the connection and try again.` for a transport failure.
  Use a retryable page error on load. Preserve user input on submit failure. Clear each pending flag in `finally`, including optimistic row state.
  A second attempt must work after a failed GET during toggle, PUT, POST, or DELETE.
- In `internal/gen/resource/nav.go:29`, remove the embed early return. For embed use `+page.svelte` **and** `form.ts` as the resource marker.
  Login and reset contain `schema.ts` and must never enter navigation. Keep the existing node marker.
  Combine disk files, planned files, and removals. Sort by href. Use the first href for home and `/` for an empty list.
- Destroy uses the unique union of both families and the existing legacy paths in `pageDestroyPaths`. Keep the existing lock hash and symlink protections.
  Never remove user edits, untracked files, or migrations. Rebuild nav and live registrations through `internal/gen/destroy/destroy.go:117`.
  Keep node-only page paths from claiming an unrelated tracked file in embed mode. For an extra family-specific candidate, require the locked template identity `gen/resource`. See `internal/gen/destroy/destroy_test.go:31`.
- In `internal/gen/page/page.go:120`, make `--server` request the mode's load file. A requested load gives role-aware projects the same CLI in both modes.
  Embed writes scaffold `web/src/routes/<route>/+page.ts` with `export const load: PageLoad` returning `{title}`.
  Use the same parent and role guards as a resource. With no auth, return the title directly. Show the correct filename in the generated Svelte component.
  Without the flag, generate only the plain component. Keep `--role` dependent on the load flag and a declared auth role.
  Preserve node's existing diagnostics and generated bytes. For embed without the flag, report `cannot generate a role-guarded page without a load function\nTry: add --server`.
  Change the CLI help and its row in `docs/site/commands/generate-page.md` to name both load files.
  The client guard controls navigation. A developer must authorize any data endpoint in Go.

### 10.6 Acceptance tests

`testdata/e2e/web/run.sh` selects `E2E_FRONTEND_MODE=node|embed`. Default to node and reject unknown values.
Pass the same variable to Playwright. Keep node's current fixture, API port, separate web port, forwarded-address configuration, and two processes.
Keep its port allocator. Select distinct free loopback ports. Exclude 55432. Embed uses one allocated API port for all three origins.
Set `PUBLIC_ORIGIN`, `E2E_BASE_URL`, and `E2E_API_URL` to that HTTP origin. Use the supplied disposable `DATABASE_URL` only.
Generate embed with `--modules ci,auth,flags,live --frontend-mode embed` and local `SPINUP_KIT_REPLACE`.
Generate `spinup g resource todo title:string done:bool 'due:date?' --live` and migrate.
In the generated web directory, run the frozen offline install, check, test, and build. The web pack has no `lint` script, and this run adds none. Build the web before compiling the tagged Go binary.
From `api/`, run `go build -tags embedweb -o ../api.bin ./cmd/api`. Start that binary with `SPINUP_API_DOCS=true`. Wait for its health check.
Use the repository's bundle check and all six specs, then the generated smoke spec. Keep browser specs in `web/e2e/` after Vitest finishes.
The existing launcher and generated workflow provide the build and cleanup examples. Run the modes serially on a disposable database. Enforce the budget immediately before serving the built app.
If the budget check fails, the launcher fails. A run without the budget check cannot satisfy the release gate.

| Spec | Assertions |
| --- | --- |
| `auth.spec.ts`, `shell.spec.ts`, `ratelimit.spec.ts`, `apidocs.spec.ts` | Keep all existing assertions in both modes. Repeat auth and shell on the embed live fixture with `--repeat-each=10`. |
| `todos.spec.ts`, two toggle waits | Before clicking, await the matching response concurrently with the click. Match the node POST action `?/toggle` at the list path or the embed PUT at the specific item's API path. Retain checkbox and pagination assertions. |
| `todos.spec.ts`, another owner's item | Node document 404. Embed document 200, client view “Page not found”, and direct owner-scoped API GET 404. Assert no other user's row appears in either mode. |
| `todos.spec.ts`, JavaScript disabled | Node submits the checkbox and persists the change. Embed serves the shell with no checkbox. Read the row through the API and verify its bool stayed unchanged. |
| `xss.spec.ts`, markup | In `helpers.ts`, parse **script-src** tokens. Accept a nonempty nonce or SHA-256 source. Reject unsafe-inline and unsafe-eval. Preserve all DOM, execution, dialog, and CSP-violation assertions. |
| `xss.spec.ts`, two documents | Node requires two distinct nonces. Embed requires the same valid SHA-256 hash across both responses. Verify it against the actual inline boot script and retain the stage 0 directives. |

Add embed cases in `auth.spec.ts`, `shell.spec.ts`, and `todos.spec.ts`. Use two pages in one browser context.
Cover account replacement, visibility recovery, API 401 recovery, failed logout, and aborted CRUD writes.
Open the live list before sign-out. Assert the exact final URL `/login`. Include a late stream 401 and a flags admin save under an authorized account.
Keep the source specs in `testdata/e2e/web/tests/`. Real navigation and an injected transport failure test the behavior that a rendering assertion cannot establish.

| Existing test | New expectation at its integration point |
| --- | --- |
| `testdata/script/embed.txtar` | Auth creation succeeds. Plain and load pages work. Resource dry-run plans browser pages without writing a migration. Retain the plain embed build and image checks. |
| `testdata/script/module_add.txtar` | Adding auth makes a review branch with browser pages and no server auth files. The main checkout stays clean. A forbidden module fails before creating state. Retain API-only refusal. |
| `testdata/script/live_module.txtar` | Embed auth plus live creates the Go wiring and browser helper. Live without explicit auth on new still fails for the missing dependency. |
| `internal/gen/newapp/newapp_test.go:538` | Exercise embed auth, flags with auth, and live with auth. Check all four exact refusals and the conflicting API-only option. |
| `internal/modules/select_test.go:89` | Add auth alone and via flags and live. Verify the dependency order, answers, each forbidden name, old archive guard, and existing jobs case. |
| `internal/gen/resource/pages_test.go:603` | Verify the eleven embed scaffolds, plain API-only plan, role and ownership variants, and absence of server files. |
| `internal/gen/page/page_test.go:144` | Check the one-file default and the two-file load variant, including title, auth, role, and no server import. |

Add the following regression files. Generated Vitest filenames and conditions are in section 10.1.

| Target | Cases |
| --- | --- |
| `internal/templates/embed_session_test.go` | Render root and watch with auth, flags, and neither. Verify conditions and no-auth bytes. |
| `internal/templates/embed_auth_test.go` | Auth with and without mail and OIDC, including the reset header wiring. Update the managed-helper expectation in `golden_auth_test.go`. |
| `internal/templates/embed_flags_live_test.go` | Verify shared schemas, browser admin calls, and live helper selection. |
| `internal/gen/resource/pages_embed_test.go` | Mixed field types, full-body toggle, both clients, guard before fetch, scoped live invalidation, and the plain no-auth family. |
| `internal/gen/resource/nav_embed_test.go` | Mix login, reset, real resources, and removals. Verify sorted nav and home. |
| `testdata/script/embed_auth_modules.txtar` | Both selection commands, all four exact errors, audit generator entry points, no writes, and recovery from a stale locked pack. |
| `testdata/script/embed_resource.txtar` | Generate, verify that repeated creation still reports the existing resource, then destroy. Verify removal of unchanged pages, retention of edits, nav rebuild, and the API-only path. |
| `auth.test.ts` | All node safe-redirect cases, guest and role guards, errors including 429, and no nested return target. |
| `session-watch.test.ts` | Parallel bootstrap, conditional flags, event coalescing, completed navigation, changed ID and role, rejected stale reads, teardown, and refresh after save. |
| `sign-out.test.ts`, `live-session.test.ts` | Record stop-before-POST ordering, duplicate calls, registration during pause, late callbacks, failed logout, resumed snapshots, and retry. |

Use single quotes for testscript quoting. Preserve existing node golden trees byte for byte.
Update only `testdata/golden/web-embed/web/vite.config.js` in the plain embed tree.
Add `TestGoldenWebEmbedAuth` in `internal/templates/golden_web_embed_auth_test.go`, selected by `PACK=web-embed-auth`.
Create `testdata/golden/web-embed-auth/` for the auth, flags, live, and ci selection.
Snapshot the selected browser twins, shared admin schema, root shell, menu, and rendered guidance. Omit duplicate UI vendor files.
Assert absence of node-only files against the **full** render before filtering. Run the deterministic render check.
The new golden is necessary because the plain embed tree exercises none of these conditional files.
Use explicit pack selection with `base,api,web,ci,auth,flags,live`. Set `app_name=todo`, `module=example.com/todo`, and the default pnpm and API client.
Enable agent guidance. Include `web/package.json`, `AGENTS.md`, and every selected output from the section 10.1 table, plus `web/src/routes/admin/flags/schema.ts`.
Use separate render cases with `mail` and `auth_oidc=true` for optional files.

### 10.7 Upgrade and digest

An upgrade renders the same locked answers against the new templates. An existing embed app without auth takes the managed Vite change.
Its layouts retain their content. It receives no auth helper, browser guard, session request, or new dependency. Normal lock provenance follows the selected sources.

Module addition renders old and new answers **at the same locked version**. See `internal/upgrade/module.go:54` and `:83`.
The planner compares disk bytes, locked hashes, and the two renders at `internal/upgrade/plan.go:176` and `:306`.
It creates missing new files. It replaces an untouched tracked scaffold. For edited text, it merges with the old render as base.
A conflict uses the existing review and continue procedure. A tracked missing file and a tombstone stay absent.
Ordinary scaffolds do participate in this process. The generator-owned and seed exceptions have separate rules.

In node mode, the layout and menu respond to the module answers this way. The auth-only menu first appears when adding auth.
Embed needs the same process for both root layouts, the home load and component, and the menu. Adding flags changes the selected shell links and flags reader.
Adding auth also updates the generated API wiring on the module branch. Review that branch before merge.
Adding auth to an app with existing public resources does not convert their database rows or retrofit generator-owned API guards. Document this boundary.
Adding auth renders the selected browser helpers directly. This requires no special scaffold overwrite.
For embed flags, replace the node wiring advice from `internal/modules/flags.go:21` with a check of the browser root load and admin link.
For embed live, give the scoped invalidation example from section 10.5. Select these instructions in `internal/upgrade/module.go` using the locked mode.
Preserve node advice. Test pristine and edited layouts and a subsequent flags addition in the review branch.

| Pack | Digest decision | Reason |
| --- | --- | --- |
| web | `none` | The existing managed config merges normally. The remaining outputs are conditional templates and new files. |
| auth | `none` | Browser files are new for a previously refused combination. Reset header wiring uses the normal template merge. |
| flags | `none` | The browser helper and admin pages first appear when this combination becomes available. |
| live | `none` | The browser helper is new for embed. Existing node helpers render identical bytes. |
| api | `none` | Only the new embed auth and mail combination enables reset header middleware. |
| deploy | `none` | The managed Caddy template merges normally. Its new clauses require live data or reset pages in embed mode. |

Refresh each changed pack's digest at its integration commit. Keep all lockfiles and kit sources outside this change.
The base `web/files/web/package.json.tmpl:14` has check and test scripts and no lint script. This run does not change `package.json.tmpl`, `pack.d/deps.toml`, or a lockfile.

### 10.8 User documentation and guidance

| File | Required change |
| --- | --- |
| `docs/embed.md` | Replace the mode and refusal tables. Describe auth, flags, live, resources, redirects, and the four refusals. Explain the observable losses below. |
| `docs/web.md` | Distinguish the universal browser load and the node server load. Describe HTTP authority, client validation, and API calls. |
| `docs/commands/module.md` | Document the release set, locked-template upgrade, review branch, and existing public resources. Remove the blanket auth refusal. |
| `docs/commands/generate-page.md` | Show both filenames for the load flag and the role guard. Replace the obsolete troubleshooting row. |
| `docs/build/flags.md` | Replace the limit at line 410. Describe public refreshes, save invalidation, admin visibility, and Go authorization. |
| `docs/build/live.md` | Replace the limit at line 385. Document scoped invalidation and stream shutdown on sign-out. |
| `docs/adr/0014-m10-contracts.md`, `docs/adr/0029-feature-flags.md`, `docs/adr/0033-live.md` | Add dated amendments pointing to section 10. Retain the historical decisions. |
| `CHANGELOG.md` | Add an Unreleased entry for embed sign-in, reset, OIDC, flags, live, resource pages, and the browser load flag. Name the four deferred modules. |
| `auth/fragments/agents-rows.md.tmpl` | For embed, qualify “Limit a page to roles” as a browser guard. Direct authors to protect the data in Go. Keep the command. |
| `web/fragments/agents-rows.md.tmpl` | For embed with auth, add “Read the signed-in user in a page” with `await parent()` in `+page.ts` and the embed guide. |

The flags and live guidance rows give valid CLI commands in both modes. Keep them. The base module-list row also applies in both modes.
Restrict both guidance changes to authenticated embed renders. The plain embed and node guidance have a byte contract.
For the first release, explain these differences in the embed guide:

- A signed-out protected document returns a public shell with 200. JavaScript redirects after reading the API. Deep-link and owner errors have a client 404 view.
- Forms require JavaScript. Validation in the browser improves feedback. Go remains responsible for validation, session, CSRF, role, and ownership.
- The shell uses a stable boot-script hash. Stage 0 already supplies `form-action`, image, font, and connection directives.
- A signed-out bootstrap produces the console 401. Another tab's auth transition discards old protected data. Failed sign-out retains the session and shows an error.
- Node server telemetry and SSR first paint do not apply. Browser trace coverage and first paint time are not checked.
- The spike measured 15,669 additional Brotli bytes. Report measurements from the release fixture separately.

The runnable block starting at `docs/embed.md:40` has two obsolete expected failures.
Replace the auth case at stage 2 with `spinup module add auth --dry-run --yes --non-interactive` inside the existing no-git fixture.
At stage 3 change the settings case to a successful dry run that lists `+page.svelte` and `+page.ts`.
The subsequent build preview depends on that fixture staying unmodified.
The node-mode “Limit a page to roles” transcript at `docs/commands/generate-page.md:81` keeps its current error and output.
The other section 7 documentation corrections are prose. No other existing runnable block requires different output for these behavior changes.

### 10.9 Deploy and budgets

The embed catch-all imports the API upstream at `internal/templates/packs/deploy/files/deploy/Caddyfile.tmpl:82`.
The default proxy discards untrusted incoming X-Forwarded-For and forwards the peer address. See the Caddy template at line 25 and ADR 0016.
The API trusts private proxy peers by default (`kit/auth/auth.go:205`). It walks the forwarded chain from the trusted end (`kit/auth/clientip.go:91`).
Thus a standard private Compose network supplies the client address to the limiter. A CDN deployment still requires the documented trusted proxy configuration.
The API service receives `PUBLIC_ORIGIN` at `internal/templates/packs/deploy/files/deploy/compose.yml.tmpl:41`.
The generated API reads it at `api/files/api/cmd/api/main.go.tmpl:102` and passes it to `auth.WithTrustedOrigins` at line 139.
Caddy replaces untrusted incoming forwarded addresses (`deploy/files/deploy/Caddyfile.tmpl:25`).
ADR 0016 records an IPv6 limitation on the default Docker bridge. Clients behind its gateway can share a bucket.
The kit calls `AddTrustedOrigin` at `kit/auth/auth.go:242`. Both checks already have the required inputs. A live TLS deploy was not checked.

Preserve ADR 0033 streaming behavior. The `/api/live/*` block at Caddy line 67 currently exists only inside the node branch.
In embed with live, emit the same API upstream with `flush_interval -1` **before** the API catch-all.
For embed with auth and mail, override the general referrer header at Caddy line 53 on the token document.
Set `Referrer-Policy: no-referrer` and `Cache-Control: no-store` on the downstream response for GET and HEAD to `/reset/<single-segment>`.
Use Caddy's deferred response header operation so the final response contains exactly one value for each header.
Keep the generated Go middleware for direct binary deployments. No change to address forwarding, Compose environment, or the kit is required.

| Check | Evidence and release decision |
| --- | --- |
| Repository e2e gate | `perf/budgets.json:2` sets 666,000 raw and 183,000 Brotli bytes. The spike used 659,850 raw and 183,348 Brotli bytes. Raw fits by 6,150. Brotli exceeds the gate by **348**. |
| Application doctor | `internal/doctor/webbundle/webbundle.go:21` sets defaults of 1,500,000 raw and 400,000 Brotli. Both spike totals fit. An app can supply stricter limits in `web/bundle-budget.json`. |
| Perf suite | `scripts/perf/suite_web.py:389` measures the node resource fixture. Its baseline is 578,548 raw and 158,359 Brotli bytes at `perf/baseline/apple-m1-max.md:191`. |
| Embed baseline | `scripts/perf/suite_web.py:529` uses ci alone with no resource. The baseline records a 35,110,130-byte binary and 53,618 requests/s at `perf/baseline/apple-m1-max.md:211`. It has no authenticated bundle baseline. |

The node limits in `perf/budgets.json` do not change. They belong to the node fixture (`ci,auth` and a `todo` resource).
The embed fixture is another app: it adds flags and live data, and its browser holds the forms, the validators, and the API client.
So the embed launcher checks against its own file, `perf/budgets-embed.json`, with the same two keys: `python3 -I scripts/perf/bundle.py check build <repo>/perf/budgets-embed.json`.
The main loop writes that file from the measured release fixture: each measured total plus 5 %, rounded up to the next 1,000 bytes.
Before that, the build units remove browser code that two files hold two times. Do not skip the check, and do not update a baseline from the spike.
The report of the run gives the measured raw and Brotli totals of both modes.
