# API reference

The kit serves a self-hosted Scalar reference at `/api/docs`.
The reference reads the OpenAPI document from `/api/openapi.yaml`.
Generated apps enable this feature with `app.WithAPIDocs(openapi.FS)`.
Without this option, the kit does not mount these routes.

In development (`SPINUP_ENV=dev`), the reference is available automatically when the app uses this option.
The kit reads `openapi/bundle.gen.yaml` from the API directory on each request.
This file takes precedence over the embedded document.
If the file is absent, the kit uses the embedded document.
A missing document returns a 404 problem with a generation instruction.

Generate an OpenAPI document in an app:

```console
$ spinup new reference --api-only --module example.com/reference --modules ci --no-git --non-interactive
...
$ cd reference
$ spinup g openapi
...
$ ls api/openapi/bundle.gen.yaml
api/openapi/bundle.gen.yaml
```

In production, the reference is disabled by default.
Set `SPINUP_API_DOCS=1` to enable it.
Production uses only the embedded document.
Restart the app after you change the setting.
Keep the reference disabled in production unless the API is public.
The docs routes do not require authentication.

For a running API with the reference enabled, request the page and the document:

<!-- doctest: skip -->
```console
$ curl http://localhost:8080/api/docs
...
$ curl http://localhost:8080/api/openapi.yaml
...
```

The reference loads no assets from a CDN.
The app serves the vendored Scalar bundle and a separate script that starts the reference.
The bundle exposes `window.Scalar.createApiReference`.
The configuration disables default fonts and telemetry.
It uses no proxy and permits the client button.
The bundle response uses gzip when the client accepts it.

Each docs response replaces the default Content Security Policy (CSP) with this policy:

```text
default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'
```

Scripts and connections must use the same origin.
The policy permits inline styles, but it blocks inline scripts and framing.
The page contains no inline script.
The document, page, and start script use `Cache-Control: no-cache`.
The Scalar bundle uses `Cache-Control: public, max-age=86400`.
In development, the `/_dev` page links to the reference when the docs routes are mounted.
