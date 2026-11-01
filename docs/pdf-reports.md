# PDF reports

A PDF report is an HTML template with print CSS.
A Python sidecar service renders it with WeasyPrint.
The Go API loads the data, checks the user, and sends the PDF to the browser.
The decisions are in [ADR 0026](adr/0026-pdf-report-sidecar.md).

## Generate the service

Run `spinup g service <name> --lang python --kind pdf` from the application root.
The flag `--kind pdf` is valid only with `--lang python`.
A typical name is `reports`.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g service reports --lang python --kind pdf
...
$ grep -E '^(memory|tmp_size) = ' spinup.toml
memory = "3g"
tmp_size = "256m"
$ grep -c 'operationId: render' services/reports/openapi.yaml
1
```

The service is a variant of the Python template.
It keeps the health check, the OpenTelemetry setup, and the `[[services]]` table of a Python service.
It adds these files. The commands run in the application of the example above.

```console
$ cd services/reports
$ find service tests -type f | sort
service/__init__.py
service/assets/report.css
service/fetcher.py
service/healthcheck.py
service/limits.py
service/main.py
service/render.py
service/render_job.py
service/telemetry.py
service/templates/sample.html.j2
tests/nosocket/sitecustomize.py
tests/test_main.py
tests/test_render.py
tests/test_telemetry.py
$ cd ../..
```

| File | Purpose |
|---|---|
| `service/main.py` | The HTTP routes, the render queue, and the child process. |
| `service/render_job.py` | The child process. It renders one request. |
| `service/render.py` | The sandboxed Jinja2 environment and the WeasyPrint call. |
| `service/fetcher.py` | The URL fetcher. It loads only `data:` URLs and files in `service/assets/`. |
| `service/limits.py` | The paths and the limits. |
| `service/templates/` | The report templates. |
| `service/assets/` | The stylesheets, images, and fonts. |

All generated files are `scaffold` files. You own them after the first run.

## The API

`POST /v1/render` takes JSON and answers `application/pdf`:

```json
{"template": "sample", "data": {"title": "March report"}}
```

The template name must match `^[a-z][a-z0-9_]{0,63}$`.
The file is `service/templates/<template>.html.j2`.
The template reads the object `data`.

| Status | Cause |
|---|---|
| 200 | The PDF. |
| 404 | The template does not exist. |
| 413 | The body is larger than the limit. |
| 422 | The body is not valid, or the template needs a key that `data` does not have. |
| 500 | The render failed, the template loads a URL that is not allowed, or the PDF is larger than 20 MiB. |
| 503 | The render queue is full. The answer has `Retry-After: 5`. |
| 504 | The render took too long. |

Each error is `application/problem+json`.
The service sets no `Content-Disposition`. The Go API sets it.

## Write a template

Add a file such as `service/templates/work_order.html.j2`.
Load the stylesheet with a relative URL. The base URL of the document is `service/assets/`.

```html
<link rel="stylesheet" href="report.css">
<h1>Work order {{ data.number }}</h1>
```

Jinja2 runs in a sandbox with autoescape and `StrictUndefined`.
A missing key fails the render with 422.
Do not use the `safe` filter. A test fails when a template uses it.

Put a brand font in `service/assets/fonts/` and load it with `@font-face`.
The image has the DejaVu and Liberation fonts.

## Call the service from the API

Run `spinup g client` after you generate the service.
The client is in `api/internal/services/reports`.
`reports.New` returns a client with a 30-second timeout.
`client.Render` returns the raw `*http.Response`.
`pdf.Serve` from the kit checks the response and writes the attachment.

The generated `routes.Deps` holds only `DB`. Add the two values that the handler needs:

```go
// api/internal/routes/routes.go
type Deps struct {
	DB      *pgxpool.Pool
	Auth    *auth.Auth                   // for RequireRole
	Reports *reports.ClientWithResponses // the PDF service client
}
```

Create the client once in `api/cmd/api/main.go`, the same way as any service client (see [Call the service from a handler](services.md#call-the-service-from-a-handler)), and set both fields where `main.go` builds `routes.Deps`.
The client reads `SERVICE_REPORTS_URL`. `spinup dev` and the deploy set it for the API, the worker, and the migrate step.
If `main.go` loads the client configuration before it runs a command, every app command needs the variable, including `api user create` and `api audit list`.
Load the configuration inside the server path, or set the variable when you run an operator command.

Then add the handler in `routes.go`, with `authn` as `d.Auth` and `reportsClient` as `d.Reports`. Use the roles of the resource:

```go
mux.Handle("GET /api/work_orders/{id}/report.pdf", authn.RequireRole(roles.Manager)(http.HandlerFunc(
	func(w http.ResponseWriter, r *http.Request) {
		// 1. Read the id. Load the row with the ownership filter of the resource.
		// 2. Build data from the row. Never forward a request body.
		data := map[string]interface{}{"number": row.Number, "title": row.Title}
		resp, err := reportsClient.Render(r.Context(), reports.RenderJSONRequestBody{Template: "work_order", Data: data})
		if err := pdf.Serve(w, resp, err, fmt.Sprintf("work-order-%d.pdf", row.Number)); err != nil {
			slog.ErrorContext(r.Context(), "render work order report", "err", err)
		}
	})))
```

The handler passes `r.Context()` to the client. The client sends `traceparent`, so one trace crosses both processes.
`pdf.Serve` takes the response and the error of `Render` and maps the errors. A sidecar 504 or a deadline gives 504. A sidecar 503 or a connection error gives 503. Any other failure gives 502.
The user sees no sidecar text. The log holds it.

If the audit module is on, you can record the download with `audit.Record` and the action `report.render`.

Add the link on the item page of the web app.
The path is on the same origin as the API, so the session cookie goes with it:

```html
<a href="/api/work_orders/42/report.pdf" download data-sveltekit-reload>Download PDF</a>
```

The generated resource pages do not get this link. You add it.

## Run the tests

The tests need the Pango libraries of the image.
Run them in a container with `spinup ci test-service reports`.
The test image adds the Debian packages of WeasyPrint.
The tests need no network.

```console
$ spinup ci test-service reports --dry-run | grep -c 'libpango-1.0-0'
1
$ spinup ci test-service reports --dry-run | tail -1
RUN uv run python -m pytest
```

The tests render the sample and read the PDF with `pypdf`.
They check each status code, the queue bound, the timeout, and the output cap.
They prove that the fetcher opens no socket, and that a render with an external URL fails.
They check that the output escapes `data`, and that the logs hold no request data.

## Security

The service is on the private `app` network. It has no public route and no authentication.
The Go API holds the authorization.

The renderer loads only `data:` URLs and files in `service/assets/`.
Any other URL fails the render, which includes `http:`, `https:`, `ftp:`, and `file:` outside the assets directory.
The `app` network has a route to the internet in this version, so the fetcher is the barrier.
A separate internal network for the sidecars is a follow-up.

Each render runs in a child process (`python -m service.render_job`).
The child has an address-space limit (`RLIMIT_AS`).
The parent kills the child at the timeout, and when the output passes 20 MiB.
The logs hold the template name, the status, and the time. They never hold `data`.

The container runs as user 10001 with a read-only root file system and a tmpfs `/tmp`.

## Limits

| Limit | Default | Change it with |
|---|---|---|
| Request body | 1 MiB | `RENDER_MAX_BODY_BYTES` |
| Time in the queue and the render | 20 seconds | `RENDER_TIMEOUT_SECONDS` |
| Address space of each render | 2 GiB | `RENDER_MAX_MEMORY_BYTES` |
| Renders at once | 2 | `RENDER_CONCURRENCY` |
| Requests that wait | 4 | `RENDER_QUEUE_MAX` |
| PDF size | 20 MiB | fixed |
| Container memory in deploy | 3 GiB | `memory` in `[[services]]` |
| Size of `/tmp` | 256 MiB | `tmp_size` in `[[services]]` |

Set the `RENDER_*` variables with `env` in the `[[services]]` table.
The `memory` of `[resources.<name>]` in `deploy/environments/<env>.toml` overrides the `memory` of the declaration.
Keep the container memory above two renders at the address-space limit.
If you raise `RENDER_CONCURRENCY` or `RENDER_MAX_MEMORY_BYTES`, raise `memory` too.

A render starts a new process. A cold render of a small page takes about 0.7 seconds.
The generated image is about 310 MB.
The unit that made the service measured these values on 2026-10-31. The run gate measures them again.

## Out of scope

- PDF/A, signatures, merges, watermarks, and encryption.
- Reports that take more than a minute. Use a job, as [Sidecar services](services.md#hand-work-from-a-job) shows.
- Pictures from a URL or from object storage.
- A generator for report handlers.
