---
title: API reference page
parent: Build your app
nav_order: 12
---

# API reference page

Your API serves an interactive reference of its own routes at `/api/docs`.
Developers can read each route and try it in the browser.
It uses [Scalar](https://scalar.com) and loads nothing from a CDN.

A new app has no bundle yet. Build it once, then start the stack and open the page:

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g openapi
...
$ ls api/openapi/bundle.gen.yaml
api/openapi/bundle.gen.yaml
```

```sh
spinup dev
# then open http://localhost:8080/api/docs
```

The web app has an "API docs" link in its sidebar, which opens the same page.
The raw document is at `/api/openapi.yaml`:

<!-- doctest: skip -->
```console
$ curl http://localhost:8080/api/openapi.yaml
openapi: 3.0.3
...
```

## Update it after you change the API

The page shows the OpenAPI bundle `api/openapi/bundle.gen.yaml`.
Run `spinup g openapi` to build the bundle from the files in `api/openapi/`.
While `spinup dev` runs, saving a file in `api/openapi/` runs this step for you.
In development the page reads the bundle on each request, so reload the page to see the change.
`spinup g resource` also writes the bundle.

If the bundle does not exist, `/api/openapi.yaml` returns a 404 that tells you to run `spinup g openapi`, and the page stays empty.

## Turn it on in production

In production the page is off. Turn it on only when the API is public.
The routes need no sign-in, so anyone who can reach the API can read them.

Add `SPINUP_API_DOCS` to the `environment` of the `api` service in `deploy/compose.yml`, then deploy again:

```yaml
services:
  api:
    environment:
      SPINUP_API_DOCS: "1"
```

Production serves the OpenAPI document that is built into the binary.
Run `spinup g openapi` and build again before you deploy an API change.

{: .note }
Edit `deploy/compose.yml` itself. A second `api` service in `deploy/compose.override.yml` makes Compose stop with a conflict.
The file `compose.yml` is managed, so `spinup upgrade` can show a conflict near your line. Keep your line when you resolve it.

The "API docs" link in the sidebar of the web app stays in place, and it leads to a 404 while the page is off.

## Check that your app has it

A generated app turns the page on in `api/cmd/api/main.go`:

```go
app.WithAPIDocs(openapi.FS),
```

Without this option, the routes do not exist.
In development, the `/_dev` page links to the reference when the routes exist.

## Security notes

- The page and the document need no sign-in. Do not put secrets in the OpenAPI files.
- The page blocks inline scripts and framing, and it connects only to its own origin.
- You cannot add a CDN script to the page.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `/api/docs` returns 404 in production | The page is off | Set `SPINUP_API_DOCS` to `1` and deploy |
| The page shows an old API in production | The binary has an old bundle | Run `spinup g openapi`, build, and deploy |
| `/api/openapi.yaml` returns a 404 that asks for a document | There is no bundle | Run `spinup g openapi` |

## See also

- [The web app](web.md)
- [generate openapi](commands/generate-openapi.md)
- [Deploy to your own server](deploy.md)
- [Sign-in security](build/auth-security.md)
