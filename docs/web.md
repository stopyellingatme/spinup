# The web app

`spinup new` creates a SvelteKit 3 app in `web/` unless you pass `--api-only`.
This page shows the layout of the app and how it talks to the Go API.

## Layout

| Path | Class | Purpose |
| --- | --- | --- |
| `web/vite.config.js` | managed | The Vite and SvelteKit options: adapter-node, `paths.origin`, the CSRF origins, Tailwind CSS, and the dev proxy. SvelteKit 3 has no `svelte.config.js`. |
| `web/tsconfig.json` | managed | Extends `$app/tsconfig`. |
| `web/package.json` | scaffold | The npm dependencies. spinup pins every version. |
| `web/pnpm-lock.yaml` | seed | The pnpm lockfile. Upgrades reinstall the dependencies and do not merge this file. |
| `web/pnpm-workspace.yaml` | managed | The pnpm settings, with the list of allowed build scripts. |
| `web/components.json` | scaffold | The shadcn-svelte settings. The aliases use `#lib`. |
| `web/src/app.html`, `app.css`, `app.d.ts` | scaffold | The page template, the Tailwind CSS entry with the theme, and the app types. |
| `web/src/env.ts` | scaffold | The server environment variables (`API_URL`). |
| `web/src/hooks.server.ts` | scaffold | Loads the user from the API and sends API requests to Go. |
| `web/src/lib/utils.ts` | scaffold | `cn()` and the helper types of the shadcn-svelte components. |
| `web/src/lib/api/client.ts` | scaffold | The typed `openapi-fetch` client. |
| `web/src/lib/api/schema.d.ts` | seed | The API types. `spinup g client` writes this file. |
| `web/src/lib/components/ui/` | seed | The shadcn-svelte components. |
| `web/src/routes/` | scaffold | The layout, the home page, and the managed `healthz` route. |
| `deploy/Dockerfile.web` | managed | The production image. |

```console
$ spinup new todo --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ ls web/src/routes
+layout.server.ts
+layout.svelte
+page.svelte
healthz
```

## One origin

The browser talks only to the web origin.
Requests to `/api/*` go to the Go API on the same origin.
Thus the session cookie of the API is a first-party cookie, and the CSRF checks of SvelteKit and Go agree.

- In development, the Vite dev server sends `/api` to `API_URL`.
- In production, Caddy sends `/api/*` to Go and all other paths to Node.
- On the server, `handleFetch` in `hooks.server.ts` sends `${PUBLIC_ORIGIN}/api/*` requests to `API_URL`. It adds the `cookie` header of the browser request only to these requests.

```console
$ grep -o "'/api': { target: apiURL, xfwd: true }" web/vite.config.js
'/api': { target: apiURL, xfwd: true }
```

## The signed-in user

For each request, `handle` in `hooks.server.ts` calls `GET ${API_URL}/api/session` with the browser cookie.
It sets `event.locals.user` to the user, or to `null`.
A 401 or a 404 response gives `null`. Without the `auth` module, the route does not exist.
If the API is not available, `handle` logs the error and continues with no user.
`+layout.server.ts` gives `locals.user` to every page as `data.user`.

## Environment

| Variable | Read at | Default | Purpose |
| --- | --- | --- | --- |
| `PUBLIC_ORIGIN` | build time | `http://localhost:5173` | The public URL. It sets `paths.origin`, `csrf.trustedOrigins`, and the `handleFetch` prefix. |
| `API_URL` | run time | `http://localhost:8080` | The address where server-side code and the dev proxy reach Go. |
| `PORT`, `HOST` | run time | `3000`, `0.0.0.0` | The listen address of `node build`. |
| `BODY_SIZE_LIMIT`, `SHUTDOWN_TIMEOUT` | run time | `1M`, `20` | The request body limit and the graceful shutdown time in seconds. |

adapter-node does not read `ORIGIN` at run time.
If the public URL changes, build the image again with `--build-arg PUBLIC_ORIGIN=<url>`.

## Commands

Run these commands in `web/`:

<!-- doctest: skip -->
```console
$ pnpm install
$ pnpm dev
$ pnpm check
$ pnpm build
$ PORT=3000 API_URL=http://localhost:8080 node build
```

## Browser tests

The web pack includes Playwright with a home-page smoke test.
The `auth` module also tests registration, login, session reload, and logout.
The smoke test scopes session controls to the header banner.
It scopes registration and login controls to the main region.
These scopes prevent duplicate links and buttons from causing strict locator errors.
The Vite configuration excludes `e2e/**` from Vitest and keeps the default exclusions.
Run `pnpm test` for unit tests and `pnpm test:e2e` for browser tests.
CI starts the API and web services before it runs the tests.
For local tests, start `spinup dev` in another terminal.

<!-- doctest: skip -->
```console
$ pnpm test:e2e
```

Set `PUBLIC_ORIGIN` when the web service uses a different URL.
The default URL is `http://localhost:5173`.

Build the image from the app root:

<!-- doctest: skip -->
```console
$ docker build -f deploy/Dockerfile.web --build-arg PUBLIC_ORIGIN=https://app.example.com web
```

The image build downloads pnpm and the npm packages, so it needs network access.

## Call the API

Pass the `fetch` of a load function or an action to the client.
Then `handleFetch` sends the request to Go with the cookie of the user:

```ts
import { api } from '#lib/api/client';

export const load = async ({ fetch }) => {
	const { data } = await api.GET('/api/todos', { fetch });
	return { todos: data ?? [] };
};
```

In the browser, the client uses the current origin.

## Upgrades

When a spinup upgrade moves `@sveltejs/kit` to a new major version, the codemod `0001_web_svmigrate` runs `pnpm dlx sv@<pin> migrate sveltekit-<major>` in `web/`.
