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
| Sign-in (`auth` module) | Yes | No |
| Server pages (`g page --server`) | Yes | No |
| Compose services | `api`, `web`, and more | `api` only |

Choose `embed` for a dashboard or a tool that needs no sign-in pages.
Choose `node` when the app needs the `auth` module or server-side loading.

## What embed cannot do

The auth pages use SvelteKit form actions, which need a server.
So embed mode refuses these commands:

```console
$ spinup g page settings --server
Error: generator "page" failed: cannot generate a server page in embed mode: +page.server.ts needs a SvelteKit server
  Try: omit --server, or use an app with --frontend-mode node
[exit 1]
$ spinup module add auth
Error: cannot use auth in embed mode: its pages require SvelteKit form actions
  Try: use --frontend-mode node, or remove auth from --modules
[exit 1]
```

`spinup g resource` also stops, because it writes server pages.
Pass `--api-only` to write only the API files.
The app reads `/api/session` from the browser, and a missing session endpoint means that nobody is signed in.

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

- A `GET` or `HEAD` request for a file returns the file. Any other path returns `index.html`, so deep links work.
- The API paths, `/healthz`, metrics, and `/version` never fall back to `index.html`. An unknown path there returns 404 for every method.
- A missing file under `/_app/` returns 404.
- Files under `/_app/immutable/` are cached for a year. `index.html` is never cached.

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
