---
title: The kit library
parent: Reference
nav_order: 5
---

# The kit library

The kit is the Go library that every generated API imports. It holds the code that you should not rewrite for each app:
the HTTP server, sessions, roles, the database pool, jobs, mail, and logging.
You call the kit from `api/cmd/api/main.go` and from your handlers.

```go
import (
    "github.com/stopyellingatme/spinup/kit/app"
    "github.com/stopyellingatme/spinup/kit/httpx"
)

func hello(w http.ResponseWriter, r *http.Request) {
    _ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"hello": "world"})
}

a := app.New(app.WithRoutes(func(mux *http.ServeMux) {
    mux.HandleFunc("GET /api/hello", hello)
}))
```

The kit has its own Go module, `github.com/stopyellingatme/spinup/kit`. Your `api/go.mod` requires it.
The kit version always matches the spinup version. See [Kit compatibility](../policy/kit-compat.md).

## Packages at a glance

| Import path | What it gives you | Section |
| --- | --- | --- |
| `kit/app` | The server: routes, middleware, health routes, commands, graceful shutdown. | [app](#app) |
| `kit/config` | Typed settings from environment variables and secret files. | [config](#config) |
| `kit/db` | The PostgreSQL pool, transactions, and migrations. | [db](#db) |
| `kit/httpx` | JSON helpers, error responses, and middleware. | [httpx](#httpx) |
| `kit/auth` | Accounts, sessions, roles, and rate limits. | [auth](#auth) |
| `kit/auth/authtest` | Test helpers that sign a user in. | [auth](#auth) |
| `kit/audit` | The audit trail: write and read the history of a record. | [audit](#audit) |
| `kit/audit/authsink` | Connects sign-in events to the audit trail. | [audit](#audit) |
| `kit/jobs` | Background jobs on PostgreSQL. | [jobs](#jobs) |
| `kit/mail` | Send email through SMTP, with a log and test mailer. | [mail](#mail) |
| `kit/pdf` | Serve a PDF from a report sidecar. | [pdf](#pdf) |
| `kit/logx` | Structured logs with redaction. | [logx](#logx) |
| `kit/secrets` | Read a secret from a variable or a file. | [secrets](#secrets) |
| `kit/telemetry` | OpenTelemetry traces, metrics, and logs. | [telemetry](#telemetry) |
| `kit/testkit` | A test database and HTTP test helpers. | [testkit](#testkit) |
| `kit/apidocs` | The API reference page at `/api/docs`. Mounted by `app.WithAPIDocs`. | |
| `kit/deployx` | The protected `/_deploy/info` route with the version and Git revision. | |
| `kit/backup` | The `/_deploy/backup` route that reports the age of the last backup. | |
| `kit/devtools` | The `/_dev` page in development. `kit/app` mounts it. | |
| `kit/deprecation` | Logs one warning for a deprecated kit path. | |

You import a package only when you use it. The last five rows work without code from you.

## app

`app.New` builds the server. A new app calls it in `api/cmd/api/main.go`.
The server always has `GET /healthz`, `GET /readyz`, and `GET /version`.
It adds a request ID, panic recovery, an access log, and security headers.

```go
a := app.New(
    app.WithName("todo"),
    app.WithVersion(version),
    app.WithConfig(cfg),
    app.WithMiddleware(authn.Middleware()),
    app.WithRoutes(func(mux *http.ServeMux) { routes.Register(mux, deps) }),
    app.WithReadiness("db", pool.Ping),
    app.WithCommand("migrate", db.MigrateCommand(migrations.FS)),
    app.WithShutdown(func(context.Context) error { pool.Close(); return nil }),
)
return a.Run(ctx, os.Args[1:])
```

| Option | What it does |
| --- | --- |
| `WithRoutes(func(*http.ServeMux))` | Adds routes. Call it more than once to add more. |
| `WithMiddleware(...)` | Adds middleware around every route. |
| `WithRequestTimeout(duration)` | Limits the request context. The default is 30 seconds. Overrides `REQUEST_TIMEOUT`. |
| `WithRequestTimeoutExempt(predicate)` | Exempts matching requests from the application deadline. Use it for streaming, SSE, or websocket routes. |
| `WithReadiness(name, check)` | Adds a check to `/readyz`. A failed check gives 503. Each check has two seconds. |
| `WithCommand(name, run)` | Adds a command such as `migrate`. See [App binary commands](app-commands.md). |
| `WithShutdown(func)` | Runs when the server stops, in reverse order. |
| `WithAPIDocs(fs)` | Serves the API reference page. In production it needs `SPINUP_API_DOCS=true`. |
| `WithSPA(fs)` | Serves a static web app from the binary. The embed mode uses it. |

`a.Handler()` returns the full handler. Use it in tests.

The request deadline includes middleware, database connection waits, and database work.
Handlers must honor context cancellation.
An expired deadline gives a 503 problem if the response has not started.
The app does not replace a response that has already started.
Devtools routes under `/_dev` are exempt in development.
Other routes have no automatic exemption.

`REQUEST_TIMEOUT` accepts a Go duration, such as `10s`.
An absent, invalid, or nonpositive value uses 30 seconds.
The app reads this variable directly during `app.New`.
For settings loaded through `config.Load`, add a duration field and pass its value to `WithRequestTimeout`.
The server write timeout uses the same configured duration.
For long-lived responses, configure transport deadlines with `http.NewResponseController` as needed.

## config

`config.Load` fills a struct from environment variables. Embed `config.Base` to get the standard settings.

```go
type Settings struct {
    config.Base
    Currency string `env:"CURRENCY" default:"USD"`
    APIKey   string `env:"PAYMENT_API_KEY" required:"true"`
}

var s Settings
if err := config.Load(&s); err != nil {
    return err
}
```

For each field, `config.Load` reads `NAME`, or `NAME_FILE` that holds a path to a file. It then uses the `default` tag.
A field with `required:"true"` and no value is an error. The error lists every problem at once and never shows a value.
In development (`SPINUP_ENV` empty or `dev`), it also reads `.env`.

`config.Base` has these fields:

| Variable | Default | What it does |
| --- | --- | --- |
| `SPINUP_ENV` | `production` | The environment name. If it is unset or `dev`, the program also reads `.env`. |
| `PORT` | `8080` | The HTTP port. |
| `DATABASE_URL` | none | The PostgreSQL connection string. |
| `DATABASE_MAX_CONNS` | pool default (20) | The size of the database pool. |
| `LOG_LEVEL` | `info` | The log level. |
| `SHUTDOWN_TIMEOUT` | `20s` | How long a graceful shutdown can take. |
| `SPINUP_API_DOCS` | `false` | Turns on the API reference outside development. |
| `SPINUP_BEHAVIOR_VERSION` | `1` | The [behavior version](../policy/behavior-version.md). |

## db

`db.Open` returns a `pgxpool.Pool` with safe timeouts. `db.InTx` runs a function in a transaction and rolls back on an error.

```go
pool, err := db.Open(ctx, cfg.DatabaseURL, db.WithMaxConns(cfg.DatabaseMaxConns))
if err != nil {
    return err
}
err = db.InTx(ctx, pool, func(tx pgx.Tx) error {
    _, err := tx.Exec(ctx, "UPDATE todos SET done = true WHERE id = $1", id)
    return err
})
```

`db.MigrateCommand(migrations.FS)` makes the `migrate` command. See [App binary commands](app-commands.md#migrate).

## httpx

`httpx` writes JSON and errors in a single format, so your clients see the same shape each time.

```go
func create(w http.ResponseWriter, r *http.Request) {
    var in struct {
        Title string `json:"title"`
    }
    if err := httpx.DecodeJSON(r, &in); err != nil {
        httpx.WriteProblem(w, r, httpx.ProblemFrom(err))
        return
    }
    if in.Title == "" {
        httpx.WriteProblem(w, r, httpx.Problem{
            Status: http.StatusUnprocessableEntity,
            Errors: []httpx.FieldError{{Field: "title", Message: "must not be empty"}},
        })
        return
    }
    _ = httpx.WriteJSON(w, http.StatusCreated, in)
}
```

`WriteProblem` writes `application/problem+json` ([RFC 9457](https://www.rfc-editor.org/rfc/rfc9457)) with the request ID.
`DecodeJSON` requires exact field names. It rejects unknown fields, duplicate names, invalid UTF-8, and extra values.
`ProblemFrom` turns its error into a 400 or 413 problem and hides the details of any other error.
Use `MaxBody` to enforce a size limit.

`CheckJSONBody(r, httpx.BodyRules{Required: required, Known: known})` validates the complete document before another decoder reads it.
It checks duplicate names at every depth and rejects invalid UTF-8.
If `Known` contains names, it rejects other names at the top level.
A non-object root returns 422. Missing or null required fields return 422 with field errors sorted by name.

Invalid JSON returns 400. A body that exceeds the size limit returns 413.
The body remains readable and retains its original `Close` method.

`RequireJSONFields(r, required)` calls `CheckJSONBody` without a list of known fields.
It validates JSON syntax even when no field is required. An empty body with empty rules is allowed.
`WriteAttachment` sends a file download. The middleware are `RequestID`, `Recover`, `AccessLog`, `SecurityHeaders`, and `MaxBody`.

## auth

`auth.New` builds the sign-in layer on your pool. It needs the tables of the auth module.

```go
authn, err := auth.New(pool,
    auth.WithRoles(roles.Set),
    auth.WithRegistration(auth.RegistrationClosed),
    auth.WithSecureCookie(cfg.Env != "dev"),
    auth.WithTrustedOrigins(publicOrigin),
)
```

Limit a route to a role with `RequireRole`. It answers 401 without a user and 403 for another role.

```go
mux.Handle("DELETE /api/todos/{id}", authn.RequireRole(roles.Manager)(deleteTodo))
```

`authn.RequireUser` needs only a signed-in user. In a handler, `auth.UserFrom(r.Context())` gives the user with `ID`, `Email`, and `Role`.
`roles.Manager` comes from the file `api/internal/roles/roles.gen.go`. See [Roles](../build/roles.md).

In a test, sign a user in without a cookie:

```go
h := authtest.AsRole(handler, 1, roles.Staff)
rec := testkit.Do(t, h, http.MethodDelete, "/api/todos/1", nil)
```

## audit

The audit trail records who changed a row. Wrap a write in `audit.InTx` so the change carries the actor.

```go
err := audit.InTx(ctx, pool, func(tx pgx.Tx) error {
    _, err := queries.WithTx(tx).UpdateTodo(ctx, params)
    return err
})
```

For a job, set a system actor first:

```go
ctx = audit.WithActor(ctx, audit.System(audit.SourceJob, "close expired todos"))
```

`audit.Record` writes an event that is not a row change. `audit.List` reads the events of one record. `audit.Prune` deletes old events.
See [Audit trail](../audit.md).

## jobs

A job is a struct with a `Kind` method. Insert it in the API. A worker process runs it.

```go
type SendDigestArgs struct {
    UserID int64 `json:"user_id"`
}

func (SendDigestArgs) Kind() string { return "send_digest" }

// In the API:
client, err := jobs.New(pool)
id, err := client.Insert(ctx, SendDigestArgs{UserID: 7}, jobs.Queue("default"), jobs.UniqueByArgs())

// In the worker (api/cmd/worker):
w, err := jobs.NewWorker(pool)
jobs.Register(w, func(ctx context.Context, job jobs.Job[SendDigestArgs]) error {
    logx.FromContext(ctx).InfoContext(ctx, "digest", "user_id", job.Args.UserID)
    return nil
})
w.Periodic(time.Hour, SendDigestArgs{})
```

`client.InsertTx` inserts a job in your own transaction. Run `spinup g job <name>` to write this code. See [Background jobs](../build/jobs.md).

## mail

`mail.New` returns an SMTP mailer. With an empty `SMTP_URL`, it returns a mailer that only logs.

```go
var cfg mail.Config
if err := config.Load(&cfg); err != nil {
    return err
}
mailer, err := mail.New(cfg)
err = mailer.Send(ctx, mail.Message{
    From: "todo@example.com", To: "ada@example.com",
    Subject: "Welcome", Text: "Hello Ada.",
})
```

| Variable | Default | What it does |
| --- | --- | --- |
| `SMTP_URL` | none | `smtp://host:1025`, `smtp+starttls://user@host:587`, or `smtps://user@host:465`. |
| `SMTP_PASSWORD` | none | The password. Never put it in the URL. |
| `SMTP_TIMEOUT` | `10s` | The time limit for one send. |

In a test, use `mail.Capture` and read `Messages()`. `mail.Render` builds a message from `name.subject.tmpl`, `name.txt.tmpl`, and `name.html.tmpl` files. See [Mail](../mail.md).

## pdf

`pdf.Serve` sends the PDF from a [report sidecar](../pdf-reports.md) to the browser and maps sidecar errors to 502, 503, or 504.

```go
resp, err := client.Render(r.Context(), reports.RenderJSONRequestBody{Template: "work_order", Data: data})
if err := pdf.Serve(w, resp, err, "work-order-42.pdf"); err != nil {
    logger.WarnContext(r.Context(), "serve the report", "error", err)
}
```

The PDF can have up to 20 MiB. The error that `Serve` returns is for your log. The user never sees it.

## logx

`logx.New` makes a `slog` logger that writes JSON, or text with `Format: "text"`. It hides secret values.
A handler can take the logger from the request context.

```go
logger := logx.New(os.Stderr, logx.Options{Level: slog.LevelInfo})
ctx = logx.WithLogger(ctx, logger)
logx.FromContext(ctx).InfoContext(ctx, "order created", "order_id", 42)
```

## secrets

`secrets.Load` reads `NAME_FILE` first and then `NAME`. A deploy puts each secret in a file, so use this for a secret that is not in a config struct.

```go
token := secrets.Must("WEBHOOK_TOKEN") // panics at start if it is missing
```

## telemetry

`telemetry.Setup` turns on traces, metrics, and logs when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Without it, nothing changes.

```go
shutdown, err := telemetry.Setup(ctx, telemetry.Options{ServiceName: "todo-api", ServiceVersion: version, Environment: cfg.Env})
if err != nil {
    return err
}
defer shutdown(context.Background())
```

Call it before `db.Open`, so the pool is traced. A deploy sets the endpoint for you. See [Monitoring](../monitoring.md).

## testkit

`testkit.DBWithMigrations` gives a test its own empty database with your migrations applied. The database is dropped when the test ends.

```go
func TestCreateTodo(t *testing.T) {
    pool := testkit.DBWithMigrations(t, migrations.FS)
    handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
        routes.Register(mux, routes.Deps{DB: pool})
    })).Handler()
    rec := testkit.Do(t, handler, http.MethodPost, "/api/todos", strings.NewReader(`{"title":"milk"}`))
    if rec.Code != http.StatusCreated {
        t.Fatalf("status %d", rec.Code)
    }
}
```

`testkit.DecodeJSON[T]` reads the response body into a type. `testkit.DB(t)` gives an empty database with no migrations.
The tests use the server in `DATABASE_URL`. That role must be able to create databases. Without `DATABASE_URL`, testkit starts a PostgreSQL container with Docker.
If neither exists, the test skips. With `CI` set, it fails instead. See [Test your app](../build/testing.md).

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `required value is missing` from `config.Load` | A required variable is not set. | Set it, or set `NAME_FILE` to a file that holds it. |
| `both X and X_FILE are set` | You set the variable and the file variable. | Keep one of the two. |
| The test skips with no database | `DATABASE_URL` is empty and Docker is not running. | Start Docker, or set `DATABASE_URL` to a server where the role can create databases. |
| `RequireRole` panics at start | The role name is not in `roles.Set`. | Use the constants of the `roles` package. |
| `audit: Bind needs a transaction` | You called `audit.Bind` on a pool. | Use `audit.InTx`. |

## See also

- [Kit compatibility](../policy/kit-compat.md): which kit changes can reach your app
- [Behavior versions](../policy/behavior-version.md): how the kit changes a default
- [App binary commands](app-commands.md): the commands built from kit packages
- [spinup.toml](configuration.md): the settings that spinup passes to the kit
