---
title: PDF reports
parent: Build your app
nav_order: 10
---

# PDF reports

You write a report as an HTML page with print CSS.
A Python [sidecar service](services.md) turns it into a PDF with WeasyPrint.
Your Go API loads the data, checks who the user is, and sends the PDF to the browser.

Create the service:

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
The flag `--kind pdf` works only with `--lang python`.
All generated files are `scaffold` files, so you own them after the first run.

## Render the sample PDF

Start the stack, then post the sample template to the service.
The service listens on its `dev_port`, which is 8101 for the first service.

<!-- doctest: skip -->
```console
$ spinup dev
$ curl -s -X POST http://127.0.0.1:8101/v1/render \
    -H 'Content-Type: application/json' \
    -d '{"template": "sample", "data": {"title": "March report", "rows": [{"label": "Orders", "value": 42}]}}' \
    -o report.pdf
$ head -c 4 report.pdf
%PDF
```

The request has two fields.
`template` names a file in `service/templates/`, without the `.html.j2` ending.
`data` is the object that the template reads.

## Write your own template

Add a file such as `service/templates/work_order.html.j2`.
The template name matches `^[a-z][a-z0-9_]{0,63}$`.
The sample template in `service/templates/sample.html.j2` is a good start. This is a shortened copy:

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>{{ data.title }}</title>
  <link rel="stylesheet" href="report.css">
</head>
<body>
  <h1>{{ data.title }}</h1>
  <table>
    {% for row in data.rows %}
    <tr><td>{{ row.label }}</td><td class="number">{{ row.value }}</td></tr>
    {% endfor %}
  </table>
</body>
</html>
```

Load the stylesheet with a relative URL, as `report.css` shows.
The base URL of the document is `service/assets/`.
To use a brand font, put it in `service/assets/fonts/` and load it with `@font-face`.
The image already has the DejaVu and Liberation fonts.

Jinja2 runs in a sandbox with autoescape on.
A key that `data` does not have fails the render with 422, instead of printing an empty value.
For an optional key, test it first: `{% if data.notes is defined %}...{% endif %}`. The sample template does this.
Do not use the `safe` filter. A test fails when a template uses it.

## Serve the PDF from the Go API

Run `spinup g client` first. It writes the client to `api/internal/services/reports/`.
`client.Render` returns the raw HTTP response.
`pdf.Serve` from the kit checks that response and writes it to the browser as an attachment.

1. Add two fields to `routes.Deps` in `api/internal/routes/routes.go`:

   ```go
   type Deps struct {
   	DB      *pgxpool.Pool
   	Auth    *auth.Auth                   // for RequireRole
   	Reports *reports.ClientWithResponses // the PDF service client
   }
   ```

2. In `api/cmd/api/main.go`, create the client the same way as for any service.
   See [Call it from Go](services.md#call-it-from-go).
   Set both fields where `main.go` builds `routes.Deps`.
3. Add the route in `routes.go`. Use the roles of your resource:

   ```go
   mux.Handle("GET /api/work_orders/{id}/report.pdf", d.Auth.RequireRole(roles.Manager)(http.HandlerFunc(
   	func(w http.ResponseWriter, r *http.Request) {
   		// 1. Read the id. Load the row with the ownership filter of the resource.
   		// 2. Build data from the row. Never forward a request body.
   		data := map[string]interface{}{"number": row.Number, "title": row.Title}
   		resp, err := d.Reports.Render(r.Context(), reports.RenderJSONRequestBody{Template: "work_order", Data: data})
   		if err := pdf.Serve(w, resp, err, fmt.Sprintf("work-order-%d.pdf", row.Number)); err != nil {
   			slog.ErrorContext(r.Context(), "render work order report", "err", err)
   		}
   	})))
   ```

`pdf.Serve` maps failures for you.
A sidecar 504 or a deadline gives the user 504. A sidecar 503 or a connection error gives 503. Any other failure gives 502.
The user sees no sidecar text. The log holds it.
Pass `r.Context()` so one trace crosses both processes.

{: .note }
The client reads `SERVICE_REPORTS_URL`. `spinup dev` and the deploy set it for the API, the worker, and the migrate step. If `main.go` creates the client before it runs a command, every app command needs that variable, including `api user create` and `api audit list`. Create the client only in the server path, or set the variable when you run an operator command.

With the [audit trail](audit.md) on, you can record each download with `audit.Record` and the action `report.render`.

## Add a download link

The API path is on the same origin as the web app, so the session cookie goes with the request.
Add the link on the item page:

```html
<a href="/api/work_orders/42/report.pdf" download data-sveltekit-reload>Download PDF</a>
```

The generated resource pages do not have this link. You add it.

## Run the tests

The tests need the Pango libraries, so run them in a container:

```console
$ spinup ci test-service reports --dry-run | grep -c 'libpango-1.0-0'
1
$ spinup ci test-service reports --dry-run | tail -1
RUN uv run python -m pytest
```

Drop `--dry-run` to run them. The tests need no network.
They render the sample and read the PDF back with `pypdf`.
They also check each status code, the queue limit, the timeout, and that a template cannot load an outside URL.
They prove that the fetcher opens no socket, that the output escapes `data`, and that the logs hold no request data.

## Reference

### The render API

`POST /v1/render` takes JSON and answers `application/pdf`.
The service sets no `Content-Disposition`. The Go API sets it.
Each error is `application/problem+json`.

| Status | Cause |
| --- | --- |
| 200 | The PDF. |
| 404 | The template does not exist. |
| 413 | The body is larger than the limit. |
| 422 | The body is not valid, or the template needs a key that `data` does not have. |
| 500 | The render failed, the template loads a URL that is not allowed, or the PDF is larger than 20 MiB. |
| 503 | The render queue is full. The answer has `Retry-After: 5`. |
| 504 | The render took too long. |

### The generated files

| File | Purpose |
| --- | --- |
| `service/main.py` | The HTTP routes and the render queue. |
| `service/render_job.py` | Renders one request in a child process. |
| `service/render.py` | The Jinja2 sandbox and the WeasyPrint call. |
| `service/fetcher.py` | Loads only `data:` URLs and files in `service/assets/`. |
| `service/limits.py` | The paths and the limits. |
| `service/templates/` | Your report templates. |
| `service/assets/` | Stylesheets, images, and fonts. |

### Limits and settings

Set the `RENDER_*` variables with the `env` table of the `[[services]]` entry in `spinup.toml`:

| Limit | Default | Change it with |
| --- | --- | --- |
| Request body | 1 MiB | `RENDER_MAX_BODY_BYTES` |
| Time in the queue and the render | 20 seconds | `RENDER_TIMEOUT_SECONDS` |
| Address space of each render | 2 GiB | `RENDER_MAX_MEMORY_BYTES` |
| Renders at once | 2 | `RENDER_CONCURRENCY` |
| Requests that wait | 4 | `RENDER_QUEUE_MAX` |
| PDF size | 20 MiB | fixed |
| Container memory in deploy | 3 GiB | `memory` in `[[services]]` |
| Size of `/tmp` | 256 MiB | `tmp_size` in `[[services]]` |

```toml
[[services]]
name = "reports"
env = { RENDER_CONCURRENCY = "3", RENDER_MAX_MEMORY_BYTES = "2147483648" }
memory = "5g"
```

Keep the container memory above the sum of the render limits.
If you raise `RENDER_CONCURRENCY` or `RENDER_MAX_MEMORY_BYTES`, raise `memory` too.
The `memory` of `[resources.<name>]` in `deploy/environments/<env>.toml` wins over the `memory` of the declaration.
A render starts a new process, so a cold render of a small page takes less than one second.

### Security in short

- The service is on the private `app` network and has no authentication. Your Go API does the authorization.
- The `app` network can reach the internet, so the fetcher below is the barrier against a template that loads an outside URL.
- A template can load only `data:` URLs and files in `service/assets/`. Any other URL, such as `https:` or `file:`, fails the render.
- Each render runs in its own child process (`python -m service.render_job`) with a memory limit. The service kills it at the timeout and when the output passes 20 MiB.
- The container runs as a non-root user with a read-only root file system and a `/tmp` tmpfs.
- The logs hold the template name, the status, and the time. They never hold `data`.

## Not supported

- PDF/A, signatures, merges, watermarks, and encryption.
- Pictures from a URL or from object storage.
- Reports that take more than a minute. Use a [background job](build/jobs.md) instead.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| The API answers 422 | The template reads a key that `data` does not have. | Add the key to `data`, or remove it from the template. |
| The API answers 503 | Too many renders wait in the queue. | Retry later, or raise `RENDER_CONCURRENCY` and `memory`. |
| The render fails with 500 | The template loads a URL that is not allowed. | Put the file in `service/assets/` and use a relative URL. |
| The tests fail on a missing library | The tests run outside the container. | Run `spinup ci test-service reports`. |

## See also

- [Sidecar services](services.md) for the client, secrets, and deploy
- [Generate a service](commands/generate-service.md)
- [Audit trail](audit.md)
- Design record: [ADR 0026](https://github.com/stopyellingatme/spinup/blob/main/docs/adr/0026-pdf-report-sidecar.md)
