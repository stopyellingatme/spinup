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
| `web/src/hooks.server.ts` | scaffold | Loads the user from the API and sends API requests to Go, with the cookie and the client address. |
| `web/src/lib/utils.ts` | scaffold | `cn()` and the helper types of the shadcn-svelte components. |
| `web/src/lib/api/client.ts` | scaffold | The typed `openapi-fetch` client. |
| `web/src/lib/api/schema.d.ts` | seed | The API types. `spinup g client` writes this file. |
| `web/src/lib/components/ui/` | seed | The shadcn-svelte components. |
| `web/src/lib/components/app/` | scaffold | The app shell parts and the form field component. |
| `web/src/lib/nav.gen.ts` | managed | The sidebar links and the home route. The resource generator owns it. |
| `web/src/routes/` | scaffold | The layout, the home page, the error page, and the managed `healthz` route. |
| `deploy/Dockerfile.web` | managed | The production image. |

```console
$ spinup new todo --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ ls web/src/routes
+error.svelte
+layout.server.ts
+layout.svelte
+page.svelte
healthz
$ ls web/src/lib/components/app
app-sidebar.svelte
field.svelte
page-header.svelte
row-menu.svelte
top-bar.svelte
```

## The app shell

`web/src/routes/+layout.svelte` gives every page the same frame:

- A top bar with the app name. With the `auth` module, it also shows the user menu (Account, Sign out) or a "Sign in" link.
- A left sidebar with the links from `nav.gen.ts`. The link of the current page has `aria-current="page"`. The link is current on its own path and on every path below it.
- An "API docs" link at the bottom of the sidebar. It opens `/api/docs` with a full page load.
- On small screens, a menu button opens the sidebar in a sheet.
- A "Skip to content" link for keyboard users, and one `<main id="main">` region.
- One `<Toaster />` for the whole app.

With the `auth` module, a visitor who is not signed in gets no sidebar.
The page content is centered, so the sign-in pages look the same as before.
Without the `auth` module, the shell has no user menu and no sign-in link.

The shell parts are in `web/src/lib/components/app/`:

| File | Purpose |
| --- | --- |
| `top-bar.svelte` | The top bar and the mobile sheet. The `actions` snippet fills the right end. |
| `app-sidebar.svelte` | The sidebar links and the "API docs" link. |
| `user-menu.svelte` | The user menu. Only apps with the `auth` module have it. |
| `page-header.svelte` | The page heading, with `title`, an optional `description`, and an `actions` snippet. |
| `field.svelte` | A form field: the label, the control, the description, and the errors. |
| `row-menu.svelte` | The actions menu of a list. A list renders one menu for all rows, and each row has a plain button that opens it. |

The resource list page (`spinup g resource`) keeps the server render cheap.
Each row has a plain button for the actions menu and a submit button for the checkbox, with inline icons.
The list renders one `RowMenu` outside the rows, and the menu opens at the button that the user chose.
The checkbox button posts its form, so it works without JavaScript.

The shadcn-svelte components use the variants `data-open:`, `data-closed:`, `data-checked:`, `data-unchecked:`, `data-disabled:`, `data-active:`, `data-horizontal:`, and `data-vertical:`.
`app.css` defines them with `@custom-variant`, as the `shadcn-svelte/tailwind.css` file of shadcn-svelte 1.7.0 does.

These files are scaffold files. Change them to fit your app. `spinup upgrade` merges your changes with new template versions.

`+error.svelte` shows the status, a short title, and a link to `home`.
It shows the error message only when the message adds information.

```console
$ grep -c 'Toaster richColors closeButton' web/src/routes/+layout.svelte
1
$ grep -c '/login' web/src/routes/+layout.svelte
0
[exit 1]
```

## Navigation and the home route

`web/src/lib/nav.gen.ts` holds the sidebar links and the home route:

```console
$ cat web/src/lib/nav.gen.ts
// Code generated by spinup. DO NOT EDIT.
export type NavItem = { href: string; label: string };
export const nav: NavItem[] = [];
export const home: string = '/';
```

Do not edit this file. `spinup g resource` with pages, and `spinup destroy resource`, write it again from the page folders under `web/src/routes/`.
The links are sorted by path. `home` is the first link, or `/` when the app has no resource pages.

When `nav` is empty, the sidebar tells you to run `spinup g resource`.
Without the `auth` module, the home page lists the `nav` links.
With the `auth` module, `web/src/routes/+page.server.ts` sends a signed-in user from `/` to `home` (status 303), when `home` is not `/`.

To add a link that the generator does not know, edit `app-sidebar.svelte`.

## Forms: the field component

`field.svelte` connects a label, a control, a description, and the errors.
It gives the control an `id` (`field-<name>` by default) and sets `aria-describedby` and `aria-invalid`.
The `children` snippet gets `{ id, describedBy, invalid, props }`.
Spread `props` on the control, or use the other values one by one:

```svelte
<script lang="ts">
	import Field from '#lib/components/app/field.svelte';
	import { Input } from '#lib/components/ui/input/index.js';

	let { errors }: { errors?: string[] } = $props();
</script>

<Field label="Title" name="title" {errors} description="A short name.">
	{#snippet children({ props })}
		<Input {...props} required />
	{/snippet}
</Field>
```

The `errors` prop takes an array of messages, such as `$errors.title` of `sveltekit-superforms`.

## Toasts

The layout renders the `svelte-sonner` toaster once, with rich colors and a close button.
Call `toast` from any component:

```ts
import { toast } from 'svelte-sonner';

toast.success('Todo saved');
```

The toasts use the theme tokens of `app.css`, so they work in light and dark mode.

## One origin

The browser talks only to the web origin.
Requests to `/api/*` go to the Go API on the same origin.
Thus the session cookie of the API is a first-party cookie, and the CSRF checks of SvelteKit and Go agree.

- In development, the Vite dev server sends `/api` to `API_URL`.
- In production, Caddy sends `/api/*` to Go and all other paths to Node.
- On the server, `handleFetch` in `hooks.server.ts` sends `${PUBLIC_ORIGIN}/api/*` requests to `API_URL`. It adds the `cookie` header of the browser request only to these requests.
- `handleFetch` also adds the client address to these requests, as the `X-Forwarded-For` header. The Go API uses it to limit the auth routes for each client.
  It deletes any `X-Forwarded-For` header from the browser first.
  It adds the address only when the `ADDRESS_HEADER` variable is set.
  If `getClientAddress()` fails, it sends no header and logs one warning for each process.

```console
$ grep -o "'/api': { target: apiURL, xfwd: true }" web/vite.config.js
'/api': { target: apiURL, xfwd: true }
$ grep -o 'ADDRESS_HEADER: .*' deploy/compose.yml
ADDRESS_HEADER: X-Forwarded-For
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
| `ADDRESS_HEADER`, `XFF_DEPTH` | run time | unset, `1` | The header with the client address and the number of proxies that wrote it. `deploy/compose.yml` sets them for the `web` service. In development, leave `ADDRESS_HEADER` unset. |

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
