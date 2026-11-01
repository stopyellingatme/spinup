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

`--server` adds a `+page.server.ts` file with a `load` function.
[Embed mode](embed.md) cannot use server pages.

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

For each request, the app asks the API who the user is (`GET /api/session`).
The result is `data.user` in every page, or `null` for a visitor.
Without the `auth` module, `data.user` is always `null`.
See [Accounts and sign-in](auth.md) to add sign-in.

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

## See also

- [Package manager and API client](web-variants.md)
- [Accounts and sign-in](auth.md)
- [Embed mode](embed.md)
- [API reference page](api-docs.md)
- [generate page](commands/generate-page.md)
