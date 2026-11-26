---
title: Embed mode
parent: Build your app
nav_order: 14
---

# Embed mode

In embed mode, the Go binary serves the API and the web app together.
You ship one binary and one container, and the browser uses one origin.
The default mode is `node`, where a separate SvelteKit server renders the pages.

```console
$ spinup new embedded --module example.com/embedded --frontend-mode embed --no-git --non-interactive
...
$ cd embedded
$ spinup g page about
Created 1, updated 0 files
Next: cd web && pnpm check
```

## Embed or node

| | `node` (default) | `embed` |
| --- | --- | --- |
| What runs | Go API and a Node server | One Go binary |
| Pages | Rendered on the server or in the browser | Static files, rendered in the browser |
| Sign-in (`auth` module) | Yes, with form actions | Yes, with forms that call the Go API |
| Feature flags (`flags` module) | Yes | Yes |
| Live data (`live` module) | Yes | Yes |
| Resource pages (`g resource`) | `+page.server.ts` and `+page.ts` | `+page.ts` and `form.ts`, in the browser |
| Pages with a load function (`g page --server`) | `+page.server.ts` | `+page.ts`, in the browser |
| Modules `apikeys`, `billing`, `audit`, `mcp` | Yes | No |
| Compose services | `api`, `web`, and more | `api` only |

Choose `embed` for a dashboard or a tool where one binary is the goal.
Choose `node` when the app needs the `apikeys`, `billing`, `audit`, or `mcp` module.
Choose `node` also when the app needs server-side loading or a Node server.

## Sign-in, flags, live data, and resources

Embed mode holds the `auth`, `flags`, and `live` modules.
Pass `--modules auth,flags,live` to `spinup new`, or run `spinup module add`.
The pages call the Go API from the browser.
The Go API stays the authority for the session, the CSRF check, validation, roles, and ownership.

A resource of an embed app gets browser pages.
This command shows them for an app with no `auth` module, so the pages have no guard:

```console
$ spinup g resource note title:string --dry-run
...
create web/src/routes/notes/+page.svelte (scaffold)
create web/src/routes/notes/+page.ts (scaffold)
...
create web/src/routes/notes/form.ts (scaffold)
...
```

- **Sign-in.** The pages `/login`, `/register`, and `/account` post JSON to `/api/auth/*`. With `--auth-oidc`, the login page also shows the provider buttons.
- **Password reset.** With the `mail` module, the pages `/reset` and `/reset/confirm` work. The mail link is `<origin>/reset/confirm#<token>`. The token is in the URL fragment, so a browser does not send it to a server. No access log, trace, proxy log, or `Referer` header holds it. The page reads the token once and removes it from the address bar. Node mode keeps the link `/reset/<token>`.
- **The session.** The root load reads `GET /api/session` and the public flags. Only a 401 means that nobody is signed in. Any other failure hides the pages and shows a card with a retry button.
- **Guards.** A page load calls `await parent()` and then `requireUser` or `requireRole` from `#lib/auth`. A guard controls navigation only. Protect the data in the Go handler.
- **Several tabs.** A sign-in, a sign-out, or a password reset in one tab makes the other tabs read the session again. A tab that sees another user replaces its document.
- **Flags.** The root reads the public flags at the start, after a session refresh, and after a successful save on `/admin/flags`. A page navigation reuses the result. See [Feature flags](build/flags.md).
- **Live data.** The helper `web/src/lib/live.svelte.ts` has the same API as in node mode. A resource made with `--live` calls `liveRefresh` with a scoped `invalidate` key, so a patch reloads only the data of that resource. See [Live data](build/live.md).
- **Resources.** `spinup g resource` writes the list, new, item, and edit pages as `+page.ts` and `+page.svelte`, with one `form.ts` for each resource. The sidebar link works as in node mode.
- **Sign-out.** Sign-out stops every live stream of the tab. Then it sends `POST /api/auth/logout`. After a 2xx answer, the page goes to `/login`. If the call fails, the user stays signed in and sees `Sign-out failed.` with the reason.

## What embed cannot do

Four modules need a SvelteKit server for their pages, so embed mode refuses them.
Each refusal happens before the command writes a file.

| Command | Error |
| --- | --- |
| `spinup module add apikeys` | `cannot use apikeys in embed mode: the API key pages require a SvelteKit server` |
| `spinup module add billing` | `cannot use billing in embed mode: the billing pages require a SvelteKit server` |
| `spinup module add audit` | `cannot use audit in embed mode: the history pages require a SvelteKit server` |
| `spinup module add mcp` | `cannot use mcp in embed mode: the module requires apikeys` |

Each error ends with `Try: use --frontend-mode node for <module>`.
`spinup g resource --audit` gives the audit error in an embed app.
`spinup new --frontend-mode embed --modules apikeys` gives the same errors.

An old app can hold a locked template version that has no browser auth pages.
Then `spinup module add auth` stops before it writes the preview:

```text
cannot add auth in embed mode: the locked templates lack the browser auth pages
Try: run spinup upgrade, then repeat the module addition
```

In an app without the `auth` module, this command shows the plan for the browser pages:

```console
$ spinup module add auth --dry-run --yes --non-interactive
Upgrade 0.0.0-dev -> 0.0.0-dev
...
  web/src/lib/session.svelte.ts
...
  web/src/routes/login/+page.ts
...
```

## What is different from node mode

Embed mode changes six things that you can see.
Read them before you choose the mode.

1. **A signed-out document returns 200.** A request for a protected path gets the public shell. JavaScript reads the API, then redirects to `/login`. A deep link to a missing item, and an item of another owner, show a 404 view in the browser.
2. **Forms need JavaScript.** The browser validates the input for fast feedback. The Go API validates again and stays responsible for the session, CSRF, roles, and ownership.
3. **The shell has a stable script hash.** The Content-Security-Policy lists the hash of the boot script in place of a nonce. The policy keeps the directives `form-action`, `img-src`, `font-src`, and `connect-src` of node mode.
4. **A signed-out start prints one console error.** The browser reports the 401 of `GET /api/session`. The Go API keeps that status. Another tab that signs out makes this tab discard its protected data. The kit has no route that clears the cookie, so a failed sign-out keeps the session.
5. **No Node server.** Node server telemetry and the first paint of server rendering do not apply. Browser trace coverage and the time of first paint are not checked.
6. **The bundle is bigger.** The browser holds the forms, the validators, and the API client. BUNDLE-TOTALS

## Build and run

`spinup build` installs the web packages with a frozen lockfile and builds the web app.
It copies the result into `api/internal/webdist/dist/` and builds the Go binary with those files inside.
Use `--dry-run` to see the steps:

```console
$ spinup build --dry-run
cd web && pnpm install --frozen-lockfile
cd web && pnpm build
Copy web/build to api/internal/webdist/dist, replacing old assets
go build -tags embedweb -trimpath -ldflags '-X main.version=dev' -o bin/api ./cmd/api
```

Run the build, then start the binary.
The binary needs a database, so start PostgreSQL first and apply the migrations.

<!-- doctest: skip -->
```console
$ docker compose -f deploy/compose.dev.yml up -d postgres
$ spinup build
$ spinup db migrate
$ SPINUP_ENV=dev ./api/bin/api
```

Open `http://localhost:8080`.
With `SPINUP_ENV` unset or set to `dev`, the binary reads `.env`, as `spinup dev` does.
The `dist/` folder is in `.gitignore`, so do not commit it.
During development, use `spinup dev`. It runs Vite with the API proxy as before.

## Build only the image

<!-- doctest: skip -->
```console
$ spinup build --image
...
```

The Docker build compiles the web app and embeds it in the API image.
If your app uses a local kit replace, spinup passes it to the build as the build context `kit`.
`spinup deploy` uses the same image, so a deploy needs no extra step:

<!-- doctest: skip -->
```console
$ spinup deploy --env staging
```

The generated Compose file has no `web` service, and Caddy sends every path to the API.
Deploy settings hold only the replicas and resources of the API.

## How the binary serves files

- A `GET` or `HEAD` request for a file returns the file. An unknown path returns `index.html`, so deep links work.
- Other methods return 405 for web paths.
- The API paths, `/healthz`, metrics, and `/version` never fall back to `index.html`. An unknown path there returns 404 for every method.
- A missing file under `/_app/` returns 404.
- Files under `/_app/immutable/` send `Cache-Control: public, max-age=31536000, immutable`. Other files, including `index.html`, send `Cache-Control: no-cache` for revalidation.

`app.WithSPA` builds a file index once, when it registers the route.
When `Accept-Encoding` permits compression, the handler selects a `.br` sibling first, then a `.gz` sibling, then the plain file.
A coding with `q=0` is refused.
An encoded response sends `Content-Encoding: br` or `Content-Encoding: gzip` and the content type of the original file.
When a file has compressed siblings, every response sends `Vary: Accept-Encoding`, including a plain response.

A compressed sibling has no public URL.
A request for its name follows the rules for a missing file.
An orphan `.br` or `.gz` file has its own public URL when no plain file exists.
The handler opens the selected file for each request and uses `http.ServeContent` with its seekable reader.
If the file has no seekable reader, the handler reads it once for that request.
Range requests for plain files remain available.

Files outside `/_app/immutable/` send a strong `ETag` from the SHA-256 hash of the plain content.
The tag contains the first 32 hexadecimal characters, inside double quotes.
Compressed variants add `.br` or `.gz` inside those quotes.
For example, a tag can be `"0123456789abcdef0123456789abcdef.br"`.
A matching `If-None-Match` returns 304 without a body.
Files under `/_app/immutable/` send no `ETag`.

At registration, the handler also hashes each inline script body in `index.html`, in document order.
Each hash covers the exact bytes between the script tags.
Scripts with a `src` attribute add no hash.
HTML responses, including the fallback and compressed variants, send this Content Security Policy:

```text
default-src 'self'; script-src 'self' <hashes>; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'
```

The directives equal the policy of node mode, with the hashes in place of the nonce (see [the web app](web.md)).
A form can post only to the origin of the app.

Each `<hashes>` entry has the form `'sha256-<base64>'`.
When `index.html` has no inline scripts, the script directive is `script-src 'self'`.
The script directive never permits `'unsafe-inline'`.
The style directive retains `'unsafe-inline'`.

The smoke spec CSP test needs the embed binary. The embed Vite dev server sends no CSP.

The managed `web/vite.config.js` sets the version name of the build to a hash of the build inputs.
The inputs are `src/`, `static/`, `package.json`, the lockfile, `vite.config.js`, and each `PUBLIC_` value.
So two builds of the same inputs give the same files, the same script hash, and the same `ETag` of `index.html`.
The default of SvelteKit is the build time, which changes these values with each build.

A Go build without the `embedweb` tag contains only a stub.
Use `spinup build` and not `go build` to get the web files.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| The binary serves no web pages | You built it with `go build` | Run `spinup build` |
| A new page returns 404 after deploy | The image has an old build | Run `spinup build --image`, then deploy again |

## See also

- [The web app](web.md)
- [Package manager and API client](web-variants.md)
- [Deploy to your own server](deploy.md)
- [build](commands/build.md)
