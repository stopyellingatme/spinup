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

Run `spinup build` to build the embedded application.
The command installs dependencies with the selected package manager and a frozen lockfile.
The command builds the web app and replaces `api/internal/webdist/dist/` with `web/build/`.
It then builds `api/bin/api` with `-tags embedweb`.
The generated `.gitignore` excludes the static assets.

<!-- doctest: skip -->
```console
$ spinup build
...
$ ./api/bin/api
...
```

For bun apps, the command uses `bun install --frozen-lockfile` and `bun run build`.
An untagged Go build uses a stub without static assets.
Development continues to use Vite and its API proxy through `spinup dev`.

Run `spinup build --image` to build only the API image.
The command supplies `--build-context web=web` to Docker buildx.
The Docker template builds the static app with the selected package manager.
It embeds the static app in the API image.
Deploy uses the same API image build.
A local kit replace is passed to the image build as the build context `kit`.

<!-- doctest: skip -->
```console
$ spinup build --image
...
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
Unknown reserved paths return 404 for every HTTP method.
Missing assets under `/_app/` return 404.

Embed mode refuses the auth module because its pages require SvelteKit form actions.
This restriction applies to `spinup new` and `spinup module add auth`.
It also refuses `g page --server` and the pages from `g resource`.
Use `--api-only` with the resource generator to generate its API files.
Use node mode when the app needs server pages or auth.

Run `make script RUN=embed` to verify an offline build and the embedded HTTP handler.
Set `SPINUP_EMBED_LIVE=1` to test the generated binary through a listening socket.
The live check does not need PostgreSQL.

Image previews and the fake Docker check run without a Docker engine.
Set `SPINUP_EMBED_DOCKER=1` for the optional Docker check.
The check skips the image build if Docker is absent.
The Docker cache must contain the images and published dependencies before this check.
