---
title: API keys and plans
parent: Build your app
nav_order: 18
---

# API keys and plans

An API key lets a program call your API without a browser sign-in.
Each user makes keys on the page `/settings/api-keys`.
A plan sets how many requests the keys of a user can make.
The `apikeys` module needs the `auth` module.

```console
$ spinup new todo --module example.com/todo --modules auth,apikeys --no-git --non-interactive
...
$ cd todo
$ spinup doctor --quick 2>/dev/null | grep apikeys-drift
ok	apikeys-drift	generated apikeys match [apikeys] in spinup.toml
```

## How a key works

A key is a Bearer token. The client sends it in the `Authorization` header.
The token looks like `key_` followed by 43 characters. You choose the first part with `prefix`.
The API stores only a digest of the token. The page shows the token one time, when you make it.

A key acts as its user, with the role of that user.
A key request has no cookie and no CSRF check.
The quota counts only key requests. Requests from the web app with a cookie are not counted.

A key has one scope:

| Scope | Methods it can use |
| --- | --- |
| `read` | `GET` and `HEAD` |
| `write` | All methods |

A key can have an expiry of 1 to 365 days. Without an expiry, the key stays valid until you revoke it.
A user can have 10 keys by default. Change the limit with `max_keys`.

A key cannot do three things, even with the `write` scope:

- It cannot call the sign-in routes under `/api/auth/`. These routes need a session.
- It cannot manage keys. The routes under `/api/me/keys` need a cookie session.
- It cannot call the admin routes under `/api/admin/`.

## Enable the module

Add the module to a new app with `--modules auth,apikeys`, as the first example shows.
To add it to an existing app, upgrade the app and then add the module. The command makes a branch for you to review.

<!-- doctest: skip -->
```console
$ spinup upgrade --yes
$ spinup module add apikeys --yes --non-interactive
...
Next: git merge spinup/module-apikeys
```

The migration goes in `api/db/kit/`, so it does not clash with your own migrations.
The command prints `Next:` lines for the files that spinup does not overwrite.
These are the edits in `api/cmd/api/main.go`. Put this block before `auth.New`:

```go
var apikeysEnv struct {
	Flush time.Duration `env:"APIKEYS_FLUSH" default:"5s"`
}
if err := config.Load(&apikeysEnv); err != nil {
	return err
}
keyStore, err := apikeys.NewStore(pool, appapikeys.Config)
if err != nil {
	return err
}
if err := keyStore.Start(ctx, apikeysEnv.Flush); err != nil {
	return err
}
```

Add `auth.WithBearer(keyStore)` to the options of `auth.New`. Then add these options to `app.New`:

```go
app.WithMiddleware(authn.Middleware()),
app.WithMiddleware(keyStore.Middleware()), // after the auth middleware
app.WithRoutes(keyStore.Routes(authn)),
app.WithCommand("apikeys", keyStore.Command()),
app.WithReadiness("apikeys", keyStore.Ready),
app.WithShutdown(keyStore.Close),
```

The key middleware must come after the auth middleware. If the flags module is on, put it after the flags middleware.
Run `spinup db migrate up` before you start the new binary. The doctor check `apikeys-wired` finds a missing line.

## Declare plans

Plans are tables in `spinup.toml`. Each plan has a limit for each minute and a limit for each day.

```toml
[apikeys]
prefix = "todo"
default_plan = "free"
max_keys = 10

[apikeys.plans.free]
per_minute = 60
per_day = 1000

[apikeys.plans.pro]
per_minute = 600
per_day = 100000
```

| Key | Default | What it does |
| --- | --- | --- |
| `prefix` | `key` | The start of each token. It has 2 to 16 lower-case letters and digits. |
| `default_plan` | `free` | The plan of a user who has no plan row. It must be a declared plan. |
| `max_keys` | `10` | The most keys a user can have. The range is 1 to 100. |
| `plans.<name>.per_minute` | none | Requests each minute, from 1 to 100000. |
| `plans.<name>.per_day` | none | Requests each UTC day, from 1 to 100000000. It is at least `per_minute`. |

Without an `[apikeys]` table, the app has one plan, `free`, with 60 requests a minute and 1000 a day.
A plan name matches `^[a-z][a-z0-9_]{0,31}$`. The app has 1 to 16 plans. An unknown key is an error.
See [the configuration reference](../reference/configuration.md#apikeys).

Run `spinup g apikeys` after each change. The command writes `api/internal/apikeys/apikeys.gen.go` and `web/src/lib/apikeys.gen.ts`.
See [generate apikeys](../commands/generate-apikeys.md).

```console
$ printf '\n[apikeys.plans.free]\nper_minute = 60\nper_day = 1000\n\n[apikeys.plans.pro]\nper_minute = 600\nper_day = 100000\n' >> spinup.toml
$ spinup g apikeys
Created 0, updated 1 files
$ grep -c PerMinute api/internal/apikeys/apikeys.gen.go
2
```

If a plan is no longer in `spinup.toml`, a user who has that plan gets the default plan. The API logs a warning for each such name.

## Call the API with a key

Make a key on the page `/settings/api-keys`, or with the [app command](../reference/app-commands.md#apikeys).
Send the token in the `Authorization` header. The route `GET /api/session` tells you which user the key acts as.

<!-- doctest: skip -->
```console
$ curl -sS -i -H "Authorization: Bearer todo_Q3n8..." https://app.example.com/api/session
HTTP/2 200
ratelimit-limit: 1000
ratelimit-remaining: 999
ratelimit-reset: 40213
...
{"user":{"email":"ada@example.com",...}}
```

Every answer to a key request has three headers:

| Header | Value |
| --- | --- |
| `RateLimit-Limit` | The requests each day that the plan allows. |
| `RateLimit-Remaining` | The requests left today. The value is never below 0. |
| `RateLimit-Reset` | The seconds until midnight UTC, when the day count restarts. |

### The answers

| Status | Cause | What to do |
| --- | --- | --- |
| 401 | The token is unknown, revoked, or expired. The answer has `WWW-Authenticate: Bearer error="invalid_token"`. | Make a new key. |
| 403 | The route is a sign-in, key, or admin route. Or a `read` key used a method other than `GET` or `HEAD`. | Use a cookie session, or make a key with the `write` scope. |
| 429 | The user is over the limit for each minute or each day. The answer has `Retry-After`. | Wait for the number of seconds in `Retry-After`. |
| 503 | The API cannot read the usage from the database. | Try again later. |

The detail of a 429 names the plan.
For the minute limit, it reads `The free plan allows 60 requests a minute. Try again in 3 seconds.`
For the day limit, it reads `The free plan allows 1000 requests a day. Try again after midnight UTC.`
For the minute limit, `Retry-After` is the wait in seconds. For the day limit, it is the seconds until midnight UTC.

A 401, a 403, and a 429 do not use the budget of the plan.
The API checks the route and the scope first. Then it checks the limit for each minute, and then the limit for each day.

## The shared budget of a user

A plan belongs to a user. All keys of the user share its budget.
A user with a `read` key and a `write` key, on the `free` plan, can make 60 requests a minute in total, not 60 for each key.
A new user has the default plan until an operator or administrator changes it.

## Limits to know

The two limits do not work the same way.

- **The limit for each minute** is in memory, in each API process. With 3 replicas, a user can make up to 3 times the `per_minute` value.
- **The limit for each day** is shared through PostgreSQL. A process writes its counts every `APIKEYS_FLUSH` (default `5s`). A user can go over the day limit by the traffic of one flush interval on each replica.

A revoke acts on the next request on every replica. The API reads the key from the database on each request.
Set `APIKEYS_FLUSH` lower, down to `1s`, for a tighter day limit. See [App binary commands](../reference/app-commands.md#settings-from-the-environment).

## The pages

| Page | Who opens it | What it shows |
| --- | --- | --- |
| `/settings/api-keys` | Every signed-in user | The plan and its limits, today's use against the day limit, the keys with their last use, and 30 days of usage. |
| `/admin/apikeys` | The `admin_role` of `[auth]` | Every account with a plan or a key, with the plan, today's use, and the keys. |

On the first page, a form makes a key from a name, a scope, and an expiry.
The page shows the token one time with the text `Copy this key now. You cannot see it again.`
A revoke asks for a confirmation. Both pages work without JavaScript.

On the second page, an administrator sets the plan of a user and revokes any key.
The user menu has the links `API keys` and, for the administrator, `API plans`.

## The operator command

Run the command from the `api/` folder. It needs no web server.
See [the apikeys command](../reference/app-commands.md#apikeys) for every option.

| Action | What it does |
| --- | --- |
| `apikeys list [--email E] [--json]` | Lists the accounts with a plan or a key, with the plan and today's use. |
| `apikeys create EMAIL --name N [--scope read\|write] [--expires-days D]` | Makes a key and prints the token one time. The default scope is `read`. |
| `apikeys revoke ID` | Revokes one key of any user. |
| `apikeys plan EMAIL PLAN` | Sets the plan of a user. |
| `apikeys usage EMAIL [--days 30] [--json]` | Prints the stored counts for each day and key. |
| `apikeys prune --days N --yes` | Deletes usage rows and revoked keys older than N days. |

<!-- doctest: skip -->
```console
$ go run ./cmd/api apikeys create ada@example.com --name reports --scope read --expires-days 90
todo_Q3n8...
This token cannot be shown again.
$ go run ./cmd/api apikeys list
ada@example.com	free	12
$ go run ./cmd/api apikeys usage ada@example.com --days 7
2026-11-06	1	12
```

## Change a plan

Run `apikeys plan`, or use the form on `/admin/apikeys`.
The new plan applies to the next key request. The user keeps the same keys.

<!-- doctest: skip -->
```console
$ go run ./cmd/api apikeys plan ada@example.com pro
updated ada@example.com	pro
```

To add a new plan, declare it in `spinup.toml`, run `spinup g apikeys`, and deploy. An unknown plan fails and prints a `Try:` line.

## Revoke and prune

To revoke a key, use the button on `/settings/api-keys`, the button on `/admin/apikeys`, or the command.
A revoked key answers 401 on the next request, on every replica.

<!-- doctest: skip -->
```console
$ go run ./cmd/api apikeys revoke 3
revoked 3
```

The usage rows grow by one row for each key and each day. Delete old rows from time to time.
The command `apikeys prune` also deletes revoked keys. It needs `--yes`, because it deletes rows for good.

<!-- doctest: skip -->
```console
$ go run ./cmd/api apikeys prune --days 90
apikeys prune: --yes is required.
Try: apikeys prune --days <n> --yes
$ go run ./cmd/api apikeys prune --days 90 --yes
pruned 42 rows
```

## The audit trail

With the [audit module](../audit.md), every change to a key or a plan writes an event.
The events never contain the token or its digest. An administrator on the admin page is the actor. The command uses the operator of the shell.
A request with a key adds the reason `api_key:<key id>` to the actor, so the history shows which key made the change.
The table of daily counts has no audit trigger, because it changes on each flush.

If you add the audit module after the apikeys module, the database has no triggers on the key tables.
The doctor check `apikeys-wired` warns about this case. Run `spinup db new audit_api_keys`, and paste these statements into the new file:

```sql
-- +goose Up
CREATE TRIGGER api_keys_audit AFTER INSERT OR UPDATE OR DELETE ON public.api_keys
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id', 'token_hash');
CREATE TRIGGER api_keys_audit_truncate AFTER TRUNCATE ON public.api_keys
    FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
CREATE TRIGGER api_key_plans_audit AFTER INSERT OR UPDATE OR DELETE ON public.api_key_plans
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'user_id');
CREATE TRIGGER api_key_plans_audit_truncate AFTER TRUNCATE ON public.api_key_plans
    FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();

-- +goose Down
DROP TRIGGER api_key_plans_audit_truncate ON public.api_key_plans;
DROP TRIGGER api_key_plans_audit ON public.api_key_plans;
DROP TRIGGER api_keys_audit_truncate ON public.api_keys;
DROP TRIGGER api_keys_audit ON public.api_keys;
```

Then run `spinup db migrate up`.

## What is out of scope

The module does not do these things:

- It does not give a key a narrower role than its user. A key acts with the full role of the user.
- It does not give a key a scope for a single route or resource.
- It does not count cookie requests, and it does not limit them. See [Auth security](auth-security.md) for the sign-in limits.
- It does not share the minute limit between replicas.
- It does not bill. It counts and limits only.

## See also

- [generate apikeys](../commands/generate-apikeys.md): the generator
- [Configuration](../reference/configuration.md#apikeys): the `[apikeys]` table
- [App binary commands](../reference/app-commands.md#apikeys): the `apikeys` command
- [Doctor checks](../reference/doctor-checks.md#apikeys-drift): `apikeys-drift` and `apikeys-wired`
- [Roles](roles.md): the role that a key acts with
- [ADR 0030](https://github.com/stopyellingatme/spinup/blob/main/docs/adr/0030-api-keys.md): the decisions
