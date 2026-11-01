# ADR 0026: PDF report sidecar

Status: accepted
Date: 2026-10-31
Extends: ADR 0022 (sidecar services), ADR 0023 (services follow-ups)

## Context

The first customer needs PDF reports: a work order sheet or an inspection summary for an insurer, or a monthly report for a client.
The user chose WeasyPrint in a Python sidecar. The report is an HTML Jinja2 template with print CSS.
The sidecar contract of ADR 0022 gives the container, the hardening, the Go client, and the tests. ADR 0023 gives OpenTelemetry in the Python template.
This ADR adds a variant of the Python template and two small kit helpers. It adds no new generator, no new command, and no Go dependency.

## Decisions

### 1. How a developer gets it

1. The command is `spinup g service <name> --lang python --kind pdf`. The flag `--kind` accepts `service` (the default) and `pdf`.
2. `--kind pdf` is valid only with `--lang python`. Any other combination fails with a `Try:` line.
   The language stays `python`. The name check at `internal/gen/service/service.go`, the health check command, `spinup ci services`, and `DetectLang` need no change. `DetectLang` still returns `python` for the PDF variant.
3. The service name follows the rules of ADR 0022. A typical name is `reports`.
4. The generator renders `templates/python` first. Then it renders `templates/python-pdf` over it.
   The current `renderTemplates` function walks one directory and calls `p.Add` for each file. `p.Add` cannot take two files with the same path.
   The unit changes it as follows:
   - It collects the files of both trees in a map keyed by the relative path. A file of the overlay tree replaces the file of the base tree.
   - It sorts the paths and calls `p.Add` once for each path.
   - It skips `.gitkeep` and any other placeholder file in both trees.
   With `--kind service`, the function walks only the base tree, so the old output stays byte-identical.
   A test checks that the PDF output has no duplicate path and no `.gitkeep`.
5. The tree `templates/python-pdf` replaces: `pyproject.toml`, `uv.lock`, `Dockerfile.tmpl`, `openapi.yaml`, `README.md.tmpl`, `service/main.py`, and `tests/test_main.py`.
   It adds: `service/render.py`, `service/render_job.py`, `service/fetcher.py`, `service/limits.py`, `service/assets/report.css`, `service/templates/sample.html.j2`, and `tests/test_render.py`.
   The templates are embedded with `//go:embed all:templates`. The tree holds no placeholder file.
6. All generated files are `scaffold`. The `[[services]]` table is the same as for any Python service.
7. The generator does not write Go code. The developer follows the recipe in decision 6.

### 2. The sidecar API

1. `GET /healthz` answers `ok`, as in the base template.
2. `POST /v1/render` takes JSON:
   ```json
   {"template": "sample", "data": {"title": "March report"}}
   ```
3. `template` matches `^[a-z][a-z0-9_]{0,63}$`. The file is `service/templates/<template>.html.j2`. No other path is possible.
4. `data` is a JSON object. The template reads it as the variable `data`.
5. A success answers 200 with `Content-Type: application/pdf`.
6. An unknown template answers 404. A bad body answers 422. A body above the limit answers 413. A render that takes too long answers 504. A render that fails answers 500.
   A full render queue answers 503 with `Retry-After` (decision 5.6). Each error is `application/problem+json`.
7. The sidecar sets no `Content-Disposition`. The Go API sets it (decision 6).
8. The operation ID is `render`. The base operation `sum` does not exist in the PDF variant.

### 3. The OpenAPI file

1. `openapi.yaml` declares `/v1/render` with the request schema `RenderRequest` and the error schema `Problem`.
2. The 200 response is:
   ```yaml
   '200':
     description: The PDF
     content:
       application/pdf:
         schema:
           type: string
           format: binary
   ```
3. oapi-codegen makes the method `Render`, which returns the raw `*http.Response`, and `RenderWithResponse`, which reads the body into `Body []byte`.
   The pre-seed confirmed this with the pinned oapi-codegen v2.8.0 (appendix). `reports.New` returns a `*ClientWithResponses`, which embeds `ClientInterface`, so `client.Render(ctx, body)` gives the raw response.
4. The kit helper uses the raw `Render` response (decision 6).
5. `TestTemplateOpenAPI` proves that the generated client compiles for the PDF variant.

### 4. The container

1. The base image is the pinned `python` image of `stackversions.yaml`.
2. The Dockerfile installs these Debian packages with `--no-install-recommends`, then removes `/var/lib/apt/lists`:
   `libpango-1.0-0`, `libpangoft2-1.0-0`, `libharfbuzz-subset0`, `fontconfig`, `fonts-dejavu-core`, and `fonts-liberation`.
   It runs `fc-cache -f` at build time.
3. The Debian packages are not pinned by version. They come from the Debian release of the pinned image. The weekly canary shows a drift.
4. The Dockerfile uses `uv sync --locked --no-dev --no-build`. A package without a wheel for the Python version fails the build at once.
5. The process runs as user 10001 with a read-only root file system and `/tmp` as tmpfs (ADR 0022). The Dockerfile sets `HOME=/tmp` and `XDG_CACHE_HOME=/tmp/cache`.
6. The image grows over the base Python image. The pre-seed probe image (the base image, the apt packages, and the locked environment) is 419 MB. The gate measures the generated image again.
7. The font set is small. A report with a brand font puts the font in `service/assets/fonts/` and loads it with `@font-face`.
8. The service has no `db` network and no secrets by default.
9. The pre-seed proved wheels for every package on Python 3.15. The PDF variant uses the pinned `python` image (3.15.2-slim-trixie). There is no second `python` image key.
   `uv sync --locked --no-build` succeeds in that image. A future pin without a wheel for the Python version fails the build at once (decision 4.4).
10. Resource limits in Compose:
    - The PDF service gets a memory limit and a size limit for the `/tmp` tmpfs. The defaults are 3 GiB of memory and a 256 MiB tmpfs.
      The memory limit is above two renders at the address-space limit of decision 5.6, and the render queue is bounded, so memory use has a ceiling.
    - The ADR 0022 Compose generator had a memory limit only for each environment: `[resources.<name>] memory` in `deploy/environments/<env>.toml` becomes `deploy.resources.limits.memory`. It had no tmpfs size: `/tmp` was fixed.
    - The pre-seed added two optional keys to `[[services]]` (`internal/services/services.go`) and to the Compose output (`internal/services/compose`):
      - `memory = "3g"`: the default memory limit of the service in deploy. The `memory` of `[resources.<name>]` in the environment file wins. The output is `deploy.resources.limits.memory`.
      - `tmp_size = "256m"`: the size of the `/tmp` tmpfs. The output is `- /tmp:size=256m`. Docker keeps its default tmpfs options: the pre-seed saw `rw,nosuid,nodev,noexec,relatime,size=262144k` in a container.
      - Both values match `^[1-9][0-9]{0,5}[kmg]$`. Validate rejects another value with a `Try:` line. An empty key renders nothing, so the Compose files of existing services stay byte-identical.
    - Lane S writes `memory = "3g"` and `tmp_size = "256m"` in the generated `[[services]]` entry of a PDF service. Lane S does not edit `internal/services`.
    - The docs state the limits and how to change them.

### 5. Security

1. The service stays on the private `app` network. The generator never sets `public`. A doctor warning is not part of this ADR.
   The `app` network is not internal, and this ADR does not make it internal (`compose.edge.yml.tmpl` in the deploy pack).
   Caddy publishes ports on it, and the API and Caddy need outbound access through it (OIDC, SMTP, ACME). Alloy also receives OTLP from the services on it.
   So the PDF container has a route to the internet in this version. The `url_fetcher` (decision 5.3) is the barrier against a fetch.
   The docs state this limit. The gate records the result of an outbound connection test from the container.
   Follow-up: a separate internal network for the sidecars. It needs a new network in `compose.edge.yml.tmpl`, the API and the worker on it, and a way for the services to reach Alloy. It is larger than this run.
2. The templates use `jinja2.sandbox.SandboxedEnvironment` with `autoescape=True`, `StrictUndefined`, and a `FileSystemLoader` on `service/templates`.
3. WeasyPrint runs with a custom `url_fetcher` in `service/fetcher.py`. WeasyPrint 70 sets these rules (appendix):
   - The `url_fetcher` must be an instance of a subclass of `weasyprint.urls.URLFetcher`. A plain function crashes the render with `AttributeError: _fail_on_errors`.
   - The subclass is created with `fail_on_errors=True`. It overrides `fetch(self, url, headers=None)`.
   - It calls `super().fetch(url, headers)` only for a `data:` URL, and for a `file:` URL whose real path (`os.path.realpath`) is inside `service/assets/`.
   - For any other URL, including `http:`, `https:`, `ftp:`, and `file:` outside the assets directory, it raises `weasyprint.urls.FatalURLFetchingError`. This makes the render fail.
   - It never raises another exception type. WeasyPrint swallows other exceptions and skips the image without an error, so a refused fetch would pass silently.
   - The render call is `HTML(string=..., base_url=..., url_fetcher=fetcher).write_pdf()`.
   - Tests prove this (decision 7.3).
4. The base URL of the document is the `service/assets/` directory.
5. The request body limit is 1 MiB. The limit is checked on `Content-Length` and again while the body is read. `RENDER_MAX_BODY_BYTES` sets it.
6. Each render runs in a child process: `python -m service.render_job`. The parent writes the request to the standard input of the child and reads the PDF from the standard output.
   - The parent kills the child after `RENDER_TIMEOUT_SECONDS` (default 20).
   - The child sets `RLIMIT_AS` to `RENDER_MAX_MEMORY_BYTES` before it imports WeasyPrint. The default is 2 GiB. The pre-seed rendered a small page under this limit. The gate tests it again, because Pango can reserve a large address space.
   - The parent reads the standard output of the child with a hard cap of 20 MiB plus one byte. If the child sends more, the parent kills it and answers 500. The parent does not wait for the end of the output to check the size.
   - The parent reads the standard error of the child with a cap of 64 KiB. It discards the rest. It logs only the capped text, and never logs `data`.
   - The parent allows 2 renders at once (`RENDER_CONCURRENCY`). The queue of waiting requests has the bound `RENDER_QUEUE_MAX` (default 4).
     When the queue is full, the parent answers 503 with `Retry-After: 5` at once, before it reads the body. A waiting request that reaches the timeout answers 504.
     The bound limits the memory of 1 MiB bodies that wait.
   - A child process is the only way to stop a CPU-bound render. A thread cannot be stopped.
   - The cost is a start time for each render. The pre-seed measured about 0.5 s for a cold render of a small page, including the WeasyPrint import. The gate measures it again.
7. The service logs the template name, the status, and the time. It never logs `data`.
8. The service has no authentication. The private network is the boundary. The Go API holds the authorization.

### 6. The Go side

1. `kit/httpx` gets `WriteAttachment(w http.ResponseWriter, filename, contentType string, body []byte)`.
   - It sets `Content-Type`, `Content-Length`, `Cache-Control: private, no-store`, and `X-Content-Type-Options: nosniff`.
   - It sets `Content-Disposition: attachment` with an ASCII `filename` and an RFC 5987 `filename*`.
   - It removes quotes, slashes, control characters, and line breaks from the file name. An empty result becomes `download`.
2. The new package `kit/pdf` stays small. It has one function: `Serve(w http.ResponseWriter, resp *http.Response, filename string) error`.
   - It closes the body.
   - It accepts only status 200 and the type `application/pdf`.
   - It reads at most 20 MiB plus one byte and checks that the body starts with `%PDF-`.
   - It calls `httpx.WriteAttachment`.
   - It maps errors: sidecar 504 or a context deadline gives 504, sidecar 503 or a connection error gives 503 with a `Try:` line, and any other failure gives 502.
     The user sees no sidecar text. The log holds it.
   - The pre-seed stub has the constant `MaxBytes` (20 MiB) and the signature. Unit K writes the body.
3. The generated Go client (`api/internal/services/reports`) plus `pdf.Serve` is enough. The generator writes no report code.
4. The recipe in `docs/pdf-reports.md` shows the handler:
   ```go
   mux.Handle("GET /api/work_orders/{id}/report.pdf", authn.RequireRole(roles.Manager)(http.HandlerFunc(
       func(w http.ResponseWriter, r *http.Request) {
           // 1. Read the id. Load the row with the ownership filter of the resource.
           // 2. Build data from the row. Never forward a request body.
           // 3. resp, err := reportsClient.Render(r.Context(), reports.RenderJSONRequestBody{Template: "work_order", Data: data})
           // 4. pdf.Serve(w, resp, "work-order-42.pdf")
       })))
   ```
5. The handler is plain `net/http` in `routes.go`. `RequireRole` is the guard (ADR 0024). The handler uses the same roles as the resource.
   `reportsClient` is the client from `reports.New(cfg)` (the generated `api/internal/services/reports/reports.go`). The generated `RenderJSONRequestBody` is `RenderRequest{Data map[string]interface{}; Template string}`.
6. The handler passes `r.Context()` to the client. The client sends `traceparent`, so one trace crosses both processes.
7. The client timeout is 30 seconds: `reports.New` already sets `http.Client.Timeout` to 30 seconds. The sidecar limit is 20 seconds, so the sidecar answers first.
8. The web download link is a plain anchor on the item page:
   `<a href="/api/work_orders/42/report.pdf" download data-sveltekit-reload>Download PDF</a>`.
   The path is on the same origin as the API, so the session cookie goes with it.
9. The generated resource pages do not get this link. The developer adds it.
10. If the audit module is on, the recipe adds one `audit.Record` call with the action `report.render` (ADR 0025 decision 4.12). It is optional.

### 7. Tests

1. `tests/test_render.py` renders `sample.html.j2`. It asserts that the body starts with `%PDF-` and that `pypdf` finds at least one page and a known text.
2. More tests assert: an unknown template gives 404, a missing data key gives 422, a body over the limit gives 413, a render over the timeout gives 504, and a full queue gives 503.
   A test with a child that writes more than 20 MiB asserts that the parent kills it and answers 500.
3. No-socket tests. The render runs in a child process, so a patch in the test process does not reach the child. Three tests cover this:
   - A unit test calls the fetcher function directly with `http:`, `https:`, `ftp:`, and `file:` URLs outside the assets directory. Each raises an error. The test patches `socket.socket` in its own process to raise, so any socket use fails the test.
   - A full-render test starts a local TCP listener on `127.0.0.1`. The template has `<img src="http://127.0.0.1:<port>/x.png">` and an external stylesheet. The render must fail and the listener must see no connection.
   - A child-level test starts the child with the same patch. The test puts a `sitecustomize.py` that replaces `socket.socket` with a function that raises on a `PYTHONPATH` directory of the test. The child inherits it, and a normal render of the sample template must still succeed.
     This proves that the normal render path opens no socket. The `sitecustomize.py` file is in `tests/`, and production code has no test hook.
4. A test with `file:///etc/passwd` asserts that the fetcher blocks it.
5. A test puts `<script>` in `data` and asserts that the output HTML escapes it. A test fails if any template contains `|safe`.
6. A test asserts that the logs hold no request data.
7. The tests run through `spinup ci test-service <name>`. They need no network.
8. The test image needs the Debian packages. The mechanism:
   - `ci.TestDockerfile(lang, stack)` in `internal/ci/services.go` stays as it is. It renders without reading files, and the five calls in `internal/ci/services_test.go` do not change.
   - The unit adds `TestDockerfileFor(lang, variant string, stack *stackversions.Stack)` and a helper `DetectVariant(dir string) string`. `TestDockerfile` calls `TestDockerfileFor(lang, "", stack)`.
   - `DetectVariant` returns `pdf` when the `pyproject.toml` in the service directory names `weasyprint`. The caller at `services.go:185` calls it and passes the result.
   - For the variant `pdf`, `TestDockerfileFor` adds the `apt-get` line with the packages of decision 4.2. For any other variant the output is byte-identical to today.
   - The tests prove: the base Python output is byte-identical, the variant `pdf` adds the line, and `DetectLang` still returns `python` for the PDF directory.
   - The file set of unit S for `internal/ci` is only `services.go` and `services_test.go`.
9. The generator tests prove the overlay: the file list, the merge by path with no duplicate and no `.gitkeep`, the pins, the generated client, and that `--kind service` output is unchanged.
10. Kit tests for `httpx.WriteAttachment` use a hostile file name with quotes and line breaks. Kit tests for `pdf.Serve` use a fake sidecar with `httptest`: a good PDF, an HTML body with a PDF type, 504, 503, and an oversize body.
11. The gate of the run plan runs the security claims for real:
    - `spinup ci test-service reports` runs the pytest suite in the Docker test image.
    - The app gate builds the image, starts it with the deploy flags, posts a render request, and checks the `%PDF-` header.
    - A render with an external image URL fails.
    - The container runs with a read-only root file system, user 10001, and a tmpfs `/tmp`. A render of the sample runs under the 2 GiB `RLIMIT_AS`.
    - The generated app route returns 401 without a session and 403 with a wrong role.

### 8. Version pins

1. `internal/stackversions/stackversions.yaml` gets a new map `python_pdf_packages` with `weasyprint`, `jinja2`, and `pypdf` (dev).
   The base map `python_packages` does not change. This keeps `TestTemplatePins` for the base template valid.
2. `internal/stackversions/stackversions.go` gets the field `PythonPDFPackages` and a call to `validateVersions` for it (near line 148). The test changes for this are part of the pre-seed.
3. `templates/python-pdf/pyproject.toml` repeats the base pins from `python_packages` and adds the new pins. All use `==`.
   `TestTemplatePins` (in unit S) checks the PDF `pyproject.toml` against both maps.
   The pre-seed commit holds `python-pdf/pyproject.toml` and `uv.lock`, so this test can run in the lane.
4. Each new direct pin must be a release at least 14 days old. Today is 2026-10-31, so the release date must be on or before 2026-10-17.
   The pins are `weasyprint==70.0` (2026-09-08), `jinja2==3.1.6` (2025-03-05), and `pypdf==6.21.1` (dev).
   The 14-day rule covers direct pins only, as for the base template. Transitive versions come from a plain `uv lock`.
   So `brotli` resolves to 1.2.1 (2026-10-22), which has cp315 wheels (1.2.0 has none), and `fonttools` resolves to 4.68.0.
5. `templates/python-pdf/uv.lock` is a shared file. The main loop made it in the pinned Python image with uv 0.13.6. Lanes never add dependencies.
6. `scripts/bump-services.sh` reads the new map in its `report` step. Its `lock` step also locks `templates/python-pdf`.
   The existing script test `testdata/script/bump_services.txtar` is updated in the pre-seed, with the script.
   The weekly canary builds the PDF image and renders the sample.

### 9. OpenTelemetry

1. The variant keeps the OTel setup of ADR 0023 from the base tree.
2. The parent process adds a span `pdf.render` with the attributes `pdf.template`, `pdf.bytes`, and `pdf.render_ms`. The child makes no span.
3. The `traceparent` from the Go client makes the server span a child of the API span.

### 10. Docs

1. `docs/pdf-reports.md` is new. It has runnable blocks: generate the service, list the files, and run the template test offline.
2. `docs/services.md` links to it. `docs/commands/generate-service.md` documents `--kind`.
3. The page states the limits: no fetch of external URLs, the 1 MiB request limit, the 20 second timeout, the queue bound, the memory limit, and the image size. The gate writes the measured image size and start time into the page.

### 11. Failure modes

| Case | Result |
|---|---|
| The sidecar is down | The API answers 503 with a `Try:` line. The log holds the error. |
| The render queue is full | The sidecar answers 503 at once. The API answers 503. |
| A render takes too long | The sidecar kills the child and answers 504. The API answers 504. |
| The template has a missing key | The sidecar answers 422. The API answers 500. This is a developer bug. |
| The data holds a hostile URL | The fetcher blocks it and the render fails. |
| The PDF is larger than the limit | The parent kills the child at the cap and the sidecar answers 500. |
| The sidecar returns HTML | `pdf.Serve` finds no `%PDF-` and answers 502. |
| A wheel is missing for the Python version | The image build fails at `uv sync --no-build` (decision 4.4). |
| The user cancels the download | The request context ends and the API closes the sidecar body. |

### 12. Upgrade of existing apps

1. An app without a PDF service does not change. The variant is a service template and not a pack, so `packs.digest` does not change.
2. Existing services keep their code (ADR 0023).
3. An app that wants reports runs `spinup g service reports --lang python --kind pdf`, then `spinup g client`, then adds the handler of decision 6.4.
4. The app needs the next kit tag for `kit/pdf` and `httpx.WriteAttachment` (ADR 0025 decision 13.6). Until the tag exists, apps in tests use `SPINUP_KIT_REPLACE`.

### 13. Out of scope

- PDF/A, signing, merging, watermarks, and encryption.
- A queue for reports that take more than a minute. The job pattern in `docs/services.md` is the recipe.
- Fetching pictures from a URL or from object storage.
- A generator for report handlers and a doctor check for them.
- A streaming PDF path. The PDF is bounded to 20 MiB and buffered.
- A public route for the service.

## Consequences

- An app gets a safe PDF path with one command for the service and about 15 lines of Go for each report.
- The image is larger than the plain Python image.
- A child process for each render gives a hard timeout and a memory limit, and it costs start time.
- The queue bound and the Compose memory limit give the service a memory ceiling.
- WeasyPrint 70 has wheels for Python 3.15 today. A later pin bump must keep wheels for the pinned Python version.
- The PDF container has a route to the internet in this version. The `url_fetcher` blocks fetches. A separate internal network is a follow-up (decision 5.1).

## Appendix: Pre-seed facts

The pre-seed (unit P0 of run 20261031-201904) and the main loop measured these facts on 2026-10-31.

### Python packages and wheels

1. Direct pins: `weasyprint==70.0`, `jinja2==3.1.6`, and `pypdf==6.21.1` (dev). They are in `python_pdf_packages` of `stackversions.yaml`.
   `templates/python-pdf/pyproject.toml` repeats the pins of `python_packages` and adds these three.
2. The main loop made `uv.lock` with uv 0.13.6 in `python:3.15.2-slim-trixie`. The pre-seed copied `pyproject.toml` and `uv.lock` into `internal/gen/service/templates/python-pdf/` without a new lock.
3. Resolved transitive versions of note: `brotli` 1.2.1 and `brotlicffi` 1.2.1.0, `fonttools` 4.68.0, `markupsafe` 3.0.4, and `pillow` 13.0.0. Each has a cp315 wheel.
4. `uv sync --locked --no-build` succeeds in the pinned image with the apt packages of decision 4.2.

### WeasyPrint 70 API

1. `HTML(string=..., base_url=..., url_fetcher=...)` and `.write_pdf()` render a document.
2. `URLFetcher.__init__(timeout=10, ssl_context=None, http_headers=None, allowed_protocols=None, allow_redirects=True, fail_on_errors=False)`.
3. `url_fetcher` must be an instance of `weasyprint.urls.URLFetcher` (or a subclass). A plain function fails with `AttributeError` on `_fail_on_errors`.
4. A subclass overrides `fetch(self, url, headers=None)`. A raised `weasyprint.urls.FatalURLFetchingError` fails the render. Any other exception is swallowed, and WeasyPrint skips the resource.

### Pre-seed measurements

1. The probe image (the base image, the apt packages, and the locked environment) is 419 MB.
2. A cold render of a small page, with the WeasyPrint import, takes about 0.5 s (0.52 s measured).
3. The render works under `RLIMIT_AS` 2 GiB, a read-only root, a tmpfs `/tmp`, user 10001, and `--network none`.
4. The gate measures the generated image again.

### oapi-codegen v2.8.0

A spec with `operationId: render`, a JSON request body `RenderRequest`, a `200` response `application/pdf` (`type: string, format: binary`), and a default `application/problem+json` response `Problem` gives:

```go
type RenderRequest struct {
	Data     map[string]interface{} `json:"data"`
	Template string                 `json:"template"`
}
type RenderJSONRequestBody = RenderRequest

func (c *Client) Render(ctx context.Context, body RenderJSONRequestBody, reqEditors ...RequestEditorFn) (*http.Response, error)
func (c *Client) RenderWithBody(ctx context.Context, contentType string, body io.Reader, reqEditors ...RequestEditorFn) (*http.Response, error)
func (c *ClientWithResponses) RenderWithResponse(ctx context.Context, body RenderJSONRequestBody, reqEditors ...RequestEditorFn) (*RenderResponse, error)
func ParseRenderResponse(rsp *http.Response) (*RenderResponse, error)

type RenderResponse struct {
	Body                          []byte
	HTTPResponse                  *http.Response
	ApplicationproblemJSONDefault *Problem
}

type ClientWithResponses struct {
	ClientInterface
}
```

The generated scaffold `New(cfg Config) (*ClientWithResponses, error)` uses an `otelhttp` transport and a 30-second `http.Client.Timeout`.
The generated package compiled.

### Compose (ruling R7)

1. `internal/services/compose` had a memory limit only through the environment file (`[resources.<name>] memory`) and a fixed `/tmp` tmpfs. The pre-seed added the optional keys `memory` and `tmp_size` (decision 4.10), with the golden file `internal/services/compose/testdata/deploy_limits.golden` and validation tests.
2. `docker compose config` accepts `/tmp:size=256m`. A container saw `tmpfs /tmp tmpfs rw,nosuid,nodev,noexec,relatime,size=262144k`.
3. The `app` network is not internal, and it cannot become internal without a larger change (decision 5.1).
