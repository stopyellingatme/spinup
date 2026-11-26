---
title: The web app
parent: Build your app
nav_order: 1
---

# The web app

`spinup new` creates a SvelteKit 3 app in `web/`, unless you pass `--api-only`.
The app has a sidebar, a form component, toasts, and a typed client for your Go API.
You change the app like any SvelteKit app.

```console
$ spinup new todo --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g page about
Created 1, updated 0 files
Next: cd web && pnpm check
```

<!-- doctest: skip -->
```console
$ spinup dev
App      http://localhost:5173
API      http://localhost:8080
Mailpit  http://localhost:8025
```

Open `http://localhost:5173`. Edit a file in `web/src` and the browser reloads.
If a port is busy, `spinup dev` picks a free port and prints it.

## What is where

You work in four places:

| Path | What you do there |
| --- | --- |
| `web/src/routes/` | Add pages. A folder is a URL. |
| `web/src/lib/components/app/` | Change the sidebar, the top bar, and the form field. |
| `web/src/lib/api/` | Call the Go API with types. |
| `web/src/app.css` | Change the theme colors. |

Files in `web/src/lib/components/ui/` come from shadcn-svelte.
Add a component with `spinup g component <name>`. This command needs the network.

Most files are `scaffold` files.
You own them, and `spinup upgrade` merges your changes with new template versions.
A few are `managed` (spinup owns them) or `seed` (spinup writes them once).
The [project layout page](get-started/project-layout.md) explains the three classes.

Do not edit these files. `spinup upgrade` writes them again.

| File | What it holds |
| --- | --- |
| `web/vite.config.js` | The Vite and SvelteKit options: adapter, public origin, CSRF origins, Tailwind CSS, and the `/api` dev proxy |
| `web/tsconfig.json` | The TypeScript settings |
| `web/pnpm-workspace.yaml` | The pnpm settings and the list of allowed build scripts |
| `web/src/lib/nav.gen.ts` | The sidebar links ([details below](#the-sidebar-and-the-home-page)) |
| `web/src/routes/healthz/+server.ts` | The health check of the web container |
| `web/playwright.config.ts` | The browser test settings |
| `deploy/Dockerfile.web` | The production image |

{: .note }
SvelteKit 3 has no `svelte.config.js`. The SvelteKit options are in `web/vite.config.js`.

The lockfile (`pnpm-lock.yaml` or `bun.lock`) and `web/src/lib/api/schema.d.ts` are seed files.
`pnpm install` and `spinup g client` update them, and an upgrade does not merge them.

## Add a page

Run `spinup g page` with a URL path.
The [guide](commands/generate-page.md) lists the route rules.

```console
$ spinup g page settings/profile --server --dry-run
create web/src/routes/settings/profile/+page.server.ts (scaffold)
create web/src/routes/settings/profile/+page.svelte (scaffold)
```

`--server` adds a file with a `load` function.
Node mode writes `+page.server.ts`, which runs on the Node server.
[Embed mode](embed.md) writes `+page.ts`, which runs in the browser.

## Add a resource with pages

`spinup g resource` writes the API, the database migration, and four pages: list, detail, new, and edit.
It also adds a link to the sidebar.

```console
$ spinup g resource todo title:string done:bool --dry-run
...
update web/src/lib/nav.gen.ts (managed)
create web/src/routes/todos/+page.server.ts (scaffold)
...
```

In embed mode, the same command writes `+page.ts` and `form.ts` files in place of the `+page.server.ts` files.

See [generate resource](commands/generate-resource.md) for the field types and the options.

## Call the API from a page

In server code, pass the `fetch` of the load function and `__PUBLIC_ORIGIN__` to the client.
Then the request carries the cookie of the signed-in user.
Node needs an absolute URL, and `__PUBLIC_ORIGIN__` supplies it.

```ts
import { api } from '#lib/api/client';

export const load = async ({ fetch }) => {
	const { data } = await api.GET('/api/todos', { fetch, baseUrl: __PUBLIC_ORIGIN__ });
	return { todos: data ?? [] };
};
```

In the browser, the client uses the current origin, so you pass neither `fetch` nor `baseUrl`.
After you change the API, run `spinup g openapi` and then `spinup g client` to update the types.
While `spinup dev` runs, saving a file in `api/openapi/` does both steps for you.
If you use heyapi, see [Package manager and API client](web-variants.md).

## The signed-in user

In node mode, the server asks the API who the user is (`GET /api/session`) for each request.
In embed mode with `auth`, the root load in the browser asks the API, and the page waits for the answer.
The result is `data.user` in every page, or `null` for a visitor.
Without the `auth` module, `data.user` is always `null`.
See [Accounts and sign-in](auth.md) to add sign-in.

In embed mode, a page that needs the user waits for the root load:

```ts
import { requireUser } from '#lib/auth';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ parent, url }) => {
	const { user } = await parent();
	return { user: requireUser(user, url) };
};
```

## Where a load runs

| | Node mode | Embed mode |
| --- | --- | --- |
| Root load | `+layout.server.ts` on the server | `+layout.ts` in the browser |
| Page load | `+page.server.ts` on the server | `+page.ts` in the browser |
| Forms | Form actions of SvelteKit | Forms with JavaScript that call the Go API |
| API calls | The server passes `fetch` and `__PUBLIC_ORIGIN__` | The browser uses its own origin |

In both modes, the Go API is the authority.
It checks the session, the CSRF check, the input, the role, and the owner of each request.
A guard in a page load controls navigation only.
Client validation with zod improves the feedback and does not replace the Go checks.

## Forms: the field component

`field.svelte` joins a label, a control, a hint, and the error messages.
It sets `id`, `aria-describedby`, and `aria-invalid` for you.
Spread `props` on the control:

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

The `errors` prop takes a list of messages, such as `$errors.title` from `sveltekit-superforms`.

## Toasts

The layout shows one toaster for the whole app.
Call `toast` from any component:

```ts
import { toast } from 'svelte-sonner';

toast.success('Todo saved');
```

## The sidebar and the home page

The sidebar links come from `web/src/lib/nav.gen.ts`.

```ts
export const nav: NavItem[] = [{ href: '/todos', label: 'Todos' }];
export const home: string = '/todos';
```

`spinup g resource` and `spinup destroy resource` write this file again.
Do not edit it, because your change is lost.
`home` is the first link, or `/` when the app has no resource pages.
With the `auth` module, a signed-in user who opens `/` goes to `home`.
A visitor who is not signed in sees no sidebar.

To add a link by hand, edit `web/src/lib/components/app/app-sidebar.svelte`.
Add a list item inside the `<ul>`, after the `{#each}` block.
The list shows only when `nav` has at least one link.

```svelte
<li><a href="/about" class={link}>About</a></li>
```

The bottom of the sidebar has an "API docs" link to [the API reference](api-docs.md).
The other shell parts are in `web/src/lib/components/app/`:

| File | What it does |
| --- | --- |
| `top-bar.svelte` | The top bar, the app name, and the menu button for small screens |
| `user-menu.svelte` | The Account and Sign out menu (only with the `auth` module) |
| `page-header.svelte` | A page heading with `title`, an optional `description`, and an `actions` snippet |
| `row-menu.svelte` | The actions menu of a list row |

`+error.svelte` shows the status of an error and a link to the home route.

## Change the look

Edit the theme tokens in `web/src/app.css`.
Change the app name in `web/src/routes/+layout.svelte`. It sets the top bar and the page title.

```css
:root {
	--primary: oklch(0.55 0.2 260);
}
```

The `.dark` block in the same file holds the dark mode colors.

## How requests reach Go

The browser talks to one origin.
Requests to `/api/*` go to the Go API, and all other requests go to SvelteKit.
So the session cookie is a first-party cookie and the CSRF checks agree.

| Where | What sends `/api` to Go |
| --- | --- |
| `spinup dev` | The Vite dev server, to `API_URL` |
| Production | Caddy |
| Server code in SvelteKit | `handleFetch` in `web/src/hooks.server.ts` |

`handleFetch` adds the `cookie` of the browser request, and only to these `/api` requests.
It also passes the client address to the API as `X-Forwarded-For`, when `ADDRESS_HEADER` is set.
The API uses the address for [rate limits](build/auth-security.md).

## Content Security Policy

Each page of the app has a `Content-Security-Policy` header.
The `csp` option of the SvelteKit plugin in the managed file `web/vite.config.js` sets the policy (ADR 0036).

| Directive | Value |
| --- | --- |
| `default-src`, `connect-src`, `base-uri`, `form-action` | `'self'` |
| `script-src` | `'self'` and a new nonce for each page |
| `style-src` | `'self' 'unsafe-inline'` |
| `img-src`, `font-src` | `'self' data:` |
| `object-src` | `'none'` |
| `frame-ancestors` | `'none'` |

SvelteKit adds the nonce to its own inline script.
The mode is `auto`, so a prerendered page gets a hash in place of a nonce.
`spinup dev` sends the same policy. The WebSocket of Vite HMR uses the same host and port, so `connect-src 'self'` allows it.
The scaffold `hooks.server.ts` sets `frame-ancestors 'none'` only on a response without a policy, such as an endpoint.

These changes need an edit of the policy, or of the page:

- A page with its own inline `<script>` needs `nonce="%sveltekit.nonce%"`. Alternatively, move the script to a module.
- An image, a font, or a `fetch` to another origin needs that origin in `img-src`, `font-src`, or `connect-src`.
- A form that posts to a page that redirects to another origin needs that origin in `form-action`. Chrome applies `form-action` to the redirect too.
- Code that calls `eval` or `new Function` fails. `web/vite.config.js` sets the `jitless` option of zod in the browser, so zod does not try `new Function`.

Do not use `{@html}` with user text. The test `TestNoRawHTML` refuses `{@html` in the templates of spinup.
In embed mode, the Go API sends the policy of `index.html`. See [Embed mode](embed.md).

## Environment variables

| Variable | When | Default | Use |
| --- | --- | --- | --- |
| `PUBLIC_ORIGIN` | build | `http://localhost:5173` | The public URL of the app |
| `API_URL` | run | `http://localhost:8080` | Where server code reaches Go |
| `PORT`, `HOST` | run | `3000`, `0.0.0.0` | Where `node build` listens |
| `BODY_SIZE_LIMIT` | run | `1M` | The largest request body |
| `SHUTDOWN_TIMEOUT` | run | `20` | Seconds to wait on shutdown |
| `ADDRESS_HEADER`, `XFF_DEPTH` | run | unset, `1` | The client address header. `deploy/compose.yml` sets them. |

{: .important }
SvelteKit reads `PUBLIC_ORIGIN` at build time.
If the public URL changes, build the image again with `--build-arg PUBLIC_ORIGIN=<url>`.

## Commands

Run these in `web/`. With bun, replace `pnpm` with `bun`.

<!-- doctest: skip -->
```console
$ pnpm install
$ pnpm dev
$ pnpm check
$ pnpm test
$ pnpm build
$ PORT=3000 API_URL=http://localhost:8080 node build
```

For `pnpm test` and the browser tests (`pnpm test:e2e`), see [Test your app](build/testing.md).

## Upgrades

`spinup upgrade` merges new template versions into your scaffold files.
When an upgrade moves `@sveltejs/kit` to a new major version, it also runs the official `sv migrate` tool in `web/`.
Review the changes with `git diff` before you commit them.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| A form post fails with "Cross-site POST form submissions are forbidden" | `PUBLIC_ORIGIN` does not match the URL in the browser | Build again with the right `PUBLIC_ORIGIN` |
| The sidebar says to run `spinup g resource` | `nav` is empty | Add a resource, or add a link by hand |
| `data.user` is `null` after sign-in | The API did not answer `/api/session` | Check that the API runs and `API_URL` is right |
| The browser console says that a script, an image, or a request "violates the following Content Security Policy directive" | The [policy](#content-security-policy) does not allow that source | Add a nonce to the script, or add the origin to the directive |

## See also

- [Package manager and API client](web-variants.md)
- [Accounts and sign-in](auth.md)
- [Embed mode](embed.md)
- [API reference page](api-docs.md)
- [generate page](commands/generate-page.md)
