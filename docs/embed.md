# Embed mode

Embed mode serves the API and a static SvelteKit app from one Go binary.
The browser uses one origin for the API and the app.

Create an app with `--frontend-mode embed`.
The default mode remains `node`.

```console
$ spinup new embedded --module example.com/embedded --frontend-mode embed --no-git --non-interactive
...
$ cd embedded
$ spinup g page about
Created 1, updated 0 files
Next: cd web && pnpm check
$ spinup g page settings --server
...
[exit 1]
```

The static adapter uses `fallback: 'index.html'`.
The root layout disables SSR with `ssr = false`.
SvelteKit 3 configures the adapter in `web/vite.config.js`.
The layout fetches `/api/session` from the browser.
An absent session endpoint means that no user has signed in.

Install the dependencies with the selected package manager.
Run the web build before the Go build.
The adapter copies `web/build/` into `api/internal/webdist/dist/` after each successful build.
It removes old assets before the copy.
The generated `.gitignore` excludes this directory.

<!-- doctest: skip -->
```console
$ cd web
$ pnpm install --frozen-lockfile --offline
$ pnpm build
$ cd ../api
$ go build -tags embedweb -o bin/api ./cmd/api
$ ./bin/api
```

For bun apps, use `bun install --frozen-lockfile` and `bun run build`.
The copy step also runs with bun.
An untagged Go build uses a stub without static assets.
Development continues to use Vite and its API proxy through `spinup dev`.

Build the Docker image with an additional context for the web app.
The Docker template builds the static app and embeds it in the API image.

<!-- doctest: skip -->
```console
$ docker build -f deploy/Dockerfile.api --build-context web=web -t embedded-api api
```

The generated Compose file has no `web` service.
Caddy sends every path to the API service.
Deploy settings contain only API replicas and resources for the app.

The handler serves assets for `GET` and `HEAD` requests.
It serves `index.html` for client routes, including deep links.
It never shows a directory listing.
Files under `/_app/immutable/` use `Cache-Control: public, max-age=31536000, immutable`.
The index uses `Cache-Control: no-cache`.
The handler permits inline bootstrap scripts in the CSP for the index.

The API prefix, health endpoints, metrics, version, and internal endpoints never use the SPA fallback.
The health endpoint is `/healthz`.
Missing assets under `/_app/` return 404.

Embed mode refuses the auth module because its pages require SvelteKit form actions.
It also refuses `g page --server` and the pages from `g resource`.
Use `--api-only` with the resource generator to generate its API files.
Use node mode when the app needs server pages or auth.

Run `make script RUN=embed` to verify an offline build and the embedded HTTP handler.
Set `SPINUP_EMBED_LIVE=1` to test the generated binary through a listening socket.
The live check does not need PostgreSQL.

The CLI build integration requires changes outside the M10.3 file set.
Until integration, run the build commands above.
The integration must build the web app and add `-tags embedweb` to the Go command.
For Docker, it must supply `--build-context web=web` and omit the separate web image.
The bun variant also needs the Docker tooling from M10.4.
