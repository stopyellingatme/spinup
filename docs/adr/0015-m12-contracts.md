# ADR 0015: M12 contracts (senior-grade defaults: ownership, app shell, forms, API docs)

Status: accepted (2026-10-16)

The first hands-on test of the dogfood app found these problems:
- Sign-in goes to `/`, not to the app.
- The only link to the edit page is the ID cell.
- No page shows the API.
- Generated resources have no access control.

M12 makes the defaults of a new app match what a senior developer ships. These decisions fix the contracts between the parallel units. ADRs 0001 to 0014 still apply.

## 1. Resource ownership (units U2 and U3)

- **When ownership applies:** the lock lists the `auth` module, and `g resource` gets no `--shared` flag. Then the resource is owned. Without the `auth` module, nothing changes, and `--shared` is an error (`Try:` drop the flag).
- **Migration:** add the column `user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE` after `id`, and the index `<table>_user_id_id_idx ON <table> (user_id, id)`.
- **Queries:** every sqlc query takes `user_id`.
  - `List<Plural>` and `Count<Plural>` filter on it.
  - `Get`, `Update`, and `Delete` add `AND user_id = $n`.
  - `Create` inserts it.
- **Handlers:**
  - They read the user with `auth.UserFrom(ctx)`. A request with no user is a programming error, because the route guard below runs first. The handler answers 401 with the problem shape.
  - A row of another user is not found. The answer is 404, never 403, so the API does not reveal that the row exists.
- **OpenAPI:**
  - `user_id` is not in any schema.
  - Every operation of an owned or shared resource with auth gains a `401` problem response.
- **Routes:** `routes_gen.go` wraps each resource handler with `auth.Required` when the app has the `auth` module, both for owned and for `--shared` resources. `routes.Deps` does not change.
- **kit `auth` additions (U2):**
  - `func Required(next http.Handler) http.Handler` answers a 401 problem when `UserFrom` finds no user. It does not need an `*Auth`, so generated code reaches it without new dependencies.
  - `func WithUser(ctx context.Context, u User) context.Context` stores a user the same way the middleware does. Tests use it.
  - The package `kit/auth/authtest` adds `func As(h http.Handler, userID int64) http.Handler`, which serves `h` with that user in the request context.
- **Generated tests:**
  - They insert two users with SQL.
  - They run CRUD through `authtest.As(handler, userA)`.
  - They assert that user B gets 404 for user A's row, and that B's list is empty.
  - They assert that a request with no user gets 401.
  - A `--shared` resource keeps today's CRUD test, run as one user, plus the 401 test.
- **Reserved names:** `g resource` and `g model` refuse a name whose plural route is `/api/docs` or `/api/openapi` (the names `doc`, `docs`, `openapi`). The error ends with `Try: choose another name, for example document`.

## 2. API reference (units U1 and U4)

- **Vendored asset:** `kit/apidocs/assets/scalar.js.gz` is the Scalar standalone bundle, 1.72.3, compressed with gzip. The main loop vendors it, and its README records the source and the checksums.
- **Package API:** `kit/apidocs` (outside the kit API guard, like `devtools`) exports:
  - `type Options struct { Spec fs.FS; Dir string; Title string }`:
    - `Spec` holds `bundle.gen.yaml` at its root.
    - When `Dir` is set, the handler reads `<Dir>/bundle.gen.yaml` from disk on each request. `Dir` wins over `Spec`.
  - `func Mount(mux *http.ServeMux, opts Options)` registers the routes below.
- **Routes:**
  - `GET /api/openapi.yaml` serves the spec as `application/yaml`. When the spec is missing, it answers a 404 problem whose detail says to run `spinup g resource` or `spinup g openapi`.
  - `GET /api/docs` serves a small HTML page.
  - `GET /api/docs/scalar.js` serves the bundle with `Content-Encoding: gzip` when the client accepts gzip. Otherwise the handler decompresses it.
  - `GET /api/docs/boot.js` serves a static boot script. It calls `Scalar.createApiReference` with:
    - `url: '/api/openapi.yaml'`
    - `withDefaultFonts: false`
    - `telemetry: false`
    - no `proxyUrl`
    - `hideClientButton: false`
  - Every docs response sets its own CSP: `default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`. Inline scripts are not allowed.
  - Cache headers: `scalar.js` uses `Cache-Control: public, max-age=86400`. The spec and HTML use `no-cache`.
- **kit `app` and `config`:**
  - `func WithAPIDocs(spec fs.FS) Option`.
  - `config.Base` gains `APIDocs bool` with `env:"SPINUP_API_DOCS"`. The default is false.
  - `New` mounts the docs when `Env == "dev"`, with `Dir: "openapi"` (the dev loop runs the API in `api/`). It also mounts them when `APIDocs` is true, with only the embedded `Spec`. Otherwise it mounts nothing, and the paths are 404.
  - The `/_dev` page links to `/api/docs` when the docs are mounted.
- **The api pack (U4):**
  - It adds the managed file `api/openapi/embed.go` (package `openapi`): `//go:embed *.yaml` and `var FS embed.FS`.
  - `main.go` passes `app.WithAPIDocs(openapi.FS)`.
  - ADR 0007 stays true: no `embedded-spec`, and no kin-openapi.

## 3. Navigation and home (units U5 and U7)

- **The nav file:** the web pack writes the managed, generator-owned file `web/src/lib/nav.gen.ts`:
  ```ts
  // Code generated by spinup. DO NOT EDIT.
  export type NavItem = { href: string; label: string };
  export const nav: NavItem[] = [];
  export const home: string = '/';
  ```
- **The rebuild (U7):** `g resource` with pages, and `destroy resource`, rebuild the file from disk.
  - Each folder `web/src/routes/<x>/` that holds the resource page set (`form.ts` beside `+page.svelte`) gives `{ href: '/<x>', label: <Title Case plural> }`.
  - The items are sorted by href.
  - `home` is the first item, or `/` when there is none.
  - The output is deterministic, uses tabs, and ends with one newline.
- **Ownership of the file:** it uses the `routes_gen.go` rules (`run.generatorOwned`, `internal/upgrade/plan.go`). The lock template is `gen/resource`.
- **The shell (U5):**
  - `web/src/routes/+layout.svelte` renders a sidebar with the `nav` items, plus an "API docs" link (`/api/docs`, `data-sveltekit-reload`) at the bottom.
  - A top bar shows the app name, plus the user menu when signed in (Account, Sign out). It shows a Sign in link when signed out.
  - On small screens, a menu button opens the sidebar in a `Sheet`.
  - The layout renders `<Toaster />` once.
  - When auth is on and no user is signed in, the layout shows no sidebar. The auth pages stay centered.
- **Other web-pack pages:**
  - `+error.svelte` shows the status, the message, and a link to `home`.
  - The pack writes `web/src/lib/components/app/` (shell parts) and `web/src/lib/components/app/field.svelte`. The field component takes a label, a name, the errors, and a description, and it ties them to the control with `id` and `aria-describedby`.

## 4. Sign-in flow (unit U6)

- **Helpers:** `web/src/lib/server/auth.ts` (managed, auth pack) adds:
  - `requireUser(locals: App.Locals, url: URL): App.Locals['user']`. When no user is signed in, it throws `redirect(303, '/login?redirectTo=' + encodeURIComponent(url.pathname + url.search))`.
  - `safeRedirect(target: string | null, fallback: string): string`. It returns `target` only for a same-site path: it starts with `/`, it does not start with `//` or `/\`, and it has no scheme. Otherwise it returns `fallback`. This copies `oidcRedirectOK` in `kit/auth/oidc.go`.
- **Redirects:**
  - Login and register go to `safeRedirect(url.searchParams.get('redirectTo'), home)`. The login form keeps `redirectTo` in its action URL.
  - When a user is already signed in, `/login` and `/register` go to `home`.
  - `/` goes to `home` for a signed-in user when `home !== '/'`.
  - Logout goes to `/login`.
  - `/account` uses `requireUser`.
- **OIDC:** the buttons pass `next=<redirectTo or home>`.

## 5. Resource pages (unit U8)

- **Files:** the pages part writes these files under `web/src/routes/<plural-kebab>/`, all `scaffold`:
  - `+page.server.ts` and `+page.svelte` (the list)
  - `new/+page.server.ts` and `new/+page.svelte`
  - `[id]/+page.server.ts` and `[id]/+page.svelte` (the read view, with the delete action)
  - `[id]/edit/+page.server.ts` and `[id]/edit/+page.svelte`
  - `schema.ts` (the zod schema, and the `toBody` and `fromItem` mappers)
  - `fields.svelte` (the shared form fields)
  - The 8 files of today change, and `[id]/edit/*` is new. `form.ts` is replaced by `schema.ts`. `Paths` lists exactly the new set, so `destroy` removes it.
- **Forms:**
  - Forms use `sveltekit-superforms` 3 with the `zod4` adapter (`sveltekit-superforms/adapters`).
  - Server actions use `superValidate`, `fail(400, { form })`, `message`, and `setError` for API problem errors.
  - On success, the action redirects with a flash parameter (`?saved=1`, `?created=1`, `?deleted=1`), and the target page shows a `toast.success`. No new flash library is used.
- **List:**
  - The first `string` field is the row link and the item title. When there is none, the link is `<Singular> <id>`.
  - A row menu (`DropdownMenu`) has View, Edit, and Delete.
  - The first non-null `bool` field shows an inline `Checkbox` that posts `?/toggle` (id, value). The action GETs the row and PUTs it with the flipped value.
  - `date` and `time` values use `Intl.DateTimeFormat`. Enum values show as a `Badge`, and bool values as a check icon.
  - Pagination uses `?page=` with 20 items per page, with `limit` and `offset` read from `total`.
  - The empty state has a call-to-action button.
- **Delete:** an `AlertDialog` on the read view and in the row menu. The form posts `?/delete`. Without JavaScript, the form post itself confirms the delete.
- **Feedback:** each submit button shows a pending state from `$submitting`.
- **Guards:** when the lock lists `auth`, every `load` and action calls `requireUser(locals, url)` first.
- **Missing parts (old apps):** before planning, the pages part checks the files the pages import (the ui components in §6 and `web/src/lib/components/app/field.svelte`) and `package.json` (`sveltekit-superforms`, `zod`, `svelte-sonner`). It names what is missing and ends with `Try: spinup upgrade (it adds the new web pack files), then run the generator again`.

## 6. Shared inputs (pre-seed, main loop)

- **npm pins** (`stackversions.yaml`): `sveltekit-superforms` 3.0.0, `zod` 4.6.5, and `svelte-sonner` 1.2.1. Superforms 3.0.0 breaks the 14-day rule, because it is the only line that supports SvelteKit 3. `formsnap` is not used: 2.0.1 needs superforms 2, which does not support SvelteKit 3.
- **shadcn-svelte 1.7.0 seed components:** alert-dialog, badge, checkbox, dropdown-menu, pagination, separator, sheet, skeleton, sonner, and table, in `pack.d/shadcn.toml`. `sonner.svelte` uses `theme="system"` instead of `mode-watcher`.
- **Upgrade path:** the new pack files are new paths, so the upgrade adds them. Edits to scaffold files are 3-way merged. The digest entries use `none`.
