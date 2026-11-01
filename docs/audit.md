# Audit trail

The `audit` module records changes to audited tables.
Each database trigger writes one event for each row that changes.
An event records the time, the actor, and the field changes.
The module requires `auth` and the web frontend.
It is off by default.

## Enable the module

For an existing app, run `spinup upgrade` first.
Then run `spinup module add audit --yes --non-interactive`.
Inspect the branch before you merge it.
The command adds the required `auth` module if necessary.
Use `--dry-run` to inspect the plan without a branch.

```console
$ spinup new ledger --module example.com/ledger --modules auth --no-git --non-interactive
...
$ cd ledger
$ spinup module add audit --dry-run --yes --non-interactive
...
```

The module adds `00006_audit_events.sql` and `history-list.svelte`.
The migration creates the event table and the trigger functions.
It attaches triggers to `users` and, when present, `user_identities`.
It redacts `password_hash`.
It does not audit `sessions`, `login_attempts`, or `password_resets`.

Check each warning about migration versions.
If an app migration has the same version, rename it to the next free number before application.
If that migration is already applied, keep its version.
Rename the new pack migration instead.
Never rename an applied migration.

The API main file has the class `scaffold`.
For an existing app, add these imports and options by hand.
The `Next:` output lists these steps.

```go
import (
    "github.com/stopyellingatme/spinup/kit/audit"
    "github.com/stopyellingatme/spinup/kit/audit/authsink"
)

// Options for auth.New(pool, ...):
auth.WithEvents(authsink.New(pool)),
auth.WithActorContext(authsink.ActorContext),

// Option for app.New(...):
app.WithCommand("audit", audit.Command(pool)),
```

Run `spinup db migrate up` before starting the new binary.
Run `spinup doctor` to inspect `audit-wired`.
The check inspects the files and needs no database connection.
If OIDC was added after audit, run `spinup g audit user_identities` to attach its triggers.

## New resources and existing tables

With the module enabled, new resources have audit triggers by default.
Use `--no-audit` to exclude one resource.
Use `--audit-redact` to redact additional columns.
The generator also redacts names that match password, passwd, secret, token, api_key, apikey, hash, or credential.
The match ignores case.
A later change to redaction requires a new migration.

```console
$ spinup new audited --module example.com/audited --modules auth,audit --no-git --non-interactive
...
$ cd audited
$ spinup g resource work_order title:string status:string --dry-run
...
$ grep -F 'auth.WithEvents(authsink.New(pool))' api/cmd/api/main.go
		auth.WithEvents(authsink.New(pool)),
```

For an existing resource, use `spinup g audit <resource>`.
The generator adds a migration, a History endpoint, and a History page.
It does not change existing handlers or detail pages.
Add a History link to `/<plural-kebab>/{id}/history` on the detail page.
Use `audit.InTx` for existing writes.
Never use `spinup g resource <name> --force` to add audit to an existing resource.
That command rewrites the create migration, which may already be applied.

The following example needs an empty database in `DATABASE_URL`.
The resource starts without audit so the retrofit can attach its triggers.

<!-- doctest: requires-env DATABASE_URL -->
```console
$ spinup g resource legacy_order title:string --no-audit
...
$ spinup g audit legacy_order
...
$ spinup db migrate up
...
$ cd api
$ go run ./cmd/api audit list --resource legacy_orders --json
...
```

The History page renders on the server and works without JavaScript.
It shows the time, the action, the actor email and role, and the field changes.
A redacted field shows `changed (value hidden)`.
An empty result shows `No history yet.`
A deleted record has no detail page.
Use the API or the operator command to read its history.

## Access and filters

History uses the same access rules as Get for the resource.
History roles replace resource roles in the role check.
Use `--history-role` to select declared roles.
By default, history uses the resource roles.

| Resource | Sign-in | Role check | Owner filter on Get | Owner filter on history |
| --- | --- | --- | --- | --- |
| Owned, no `--role` | yes | none | `WHERE id = $1 AND user_id = $2` | `owner_id = user.ID` |
| Owned, `--role R` | yes | `user.HasRole(R...)`, else 403 | the same `user_id` filter for every role | `owner_id = user.ID` for every role |
| `--shared`, no `--role` | yes | none | none | none |
| `--shared --role R` | yes | `user.HasRole(R...)`, else 403 | none | none |
| No auth module | none | none | none | unavailable because audit requires auth |

No role bypasses the owner filter, including the admin role.
History uses the stored owner ID, so deletion does not remove access to past events.
A request with no matching events returns 200 with an empty list.
The response sets `Cache-Control: no-store`.

`GET /api/<plural>/{id}/history` accepts `limit` and `offset`.
The default limit is 50, and the maximum is 200.
The maximum offset is 10,000.
A value above a maximum returns 422 with a `Try:` line.
Use a cursor or a narrower range in `audit.List` for larger queries.
Only resource history has an HTTP endpoint.
Use the operator command for auth events and other tables.

## Operator command

The app binary reads `DATABASE_URL` and needs no web server.
The command never prompts.
A missing flag produces an error with a `Try:` line.

Use `audit list --resource TABLE` to read recent events.
Optional filters are `--id`, `--actor`, `--action`, `--since`, `--until`, and `--unattributed`.
Use `--json` for structured output.
The default limit is 100, and the maximum is 1000.

<!-- doctest: requires-env DATABASE_URL -->
```console
$ go run ./cmd/api audit list --resource users --limit 100 --json
...
$ go run ./cmd/api audit list --resource users --unattributed
...
$ go run ./cmd/api audit export --since 2000-01-01 --until 2100-01-01 --format jsonl --out audit.jsonl
...
$ test -f audit.jsonl
```

Export accepts `jsonl` or `csv`.
It reads pages of 1000 events and writes each page immediately.
It prints a SHA-256 digest for verification.
The trail does not record command reads.

## Handler recipe

For each write, use `audit.InTx` and `s.Queries.WithTx(tx)`.
The auth middleware supplies the signed-in actor through `authsink.ActorContext`.
The transaction binds that actor before the data statement runs.

```go
err := audit.InTx(ctx, s.DB, func(tx pgx.Tx) error {
    _, err := s.Queries.WithTx(tx).UpdateWorkOrder(ctx, params)
    return err
})
```

For a job, bind a system actor in the context before the transaction.
Use a reason that explains the batch.

```go
ctx = audit.WithActor(ctx, audit.System(audit.SourceJob, "close expired orders"))
err := audit.InTx(ctx, pool, func(tx pgx.Tx) error {
    _, err := tx.Exec(ctx, "UPDATE work_orders SET status = $1 WHERE status = $2", "closed", "expired")
    return err
})
```

Call `audit.Record` for an event that is not a row change.
For a handwritten history route, copy the generated guard, owner filter, and limits before calling `audit.List`.
A write without a bound actor still records the change as `unattributed`.
If a row event fails, the data statement fails and the transaction rolls back.
If a sign-in event fails, auth logs the error and the user continues.

## Retention and size

The default retention is forever.
The trail never prunes automatically.
Keep row history unless your retention policy requires deletion.
Consider an age limit for sign-in events.
The prune command deletes all events before its cutoff, including row history.
It has no action filter.
Export and review the affected range before pruning it.

Prune refuses a cutoff less than one day old.
It requires a reason and `--yes`.
It writes an `audit.prune` event with the range, count, and reason.

<!-- doctest: requires-env DATABASE_URL -->
```console
$ go run ./cmd/api audit prune --before 2000-01-01 --reason 'retention policy review' --yes
...
```

Allow approximately 1 KB per event, including indexes.
At 10,000 events per day, allow approximately 3.6 GB per year.
Batch large updates and supply a reason.
An update of N rows creates N events.
Backups include the trail because it uses the app database.

## Limits and hardening

The guard trigger prevents accidental updates, deletes, and truncates on `audit_events`.
It does not prevent tampering by application code or the database owner.
The default application user owns the tables.
That user can enable the prune setting, call `audit_prune`, disable triggers, or edit events.
The trail is not tamper-proof.
It has no hash chain or external anchor.

For stronger protection, use a separate migration role that owns the tables and functions.
Give the app role only `INSERT` and `SELECT` on `audit_events`.
Make prune and the trigger functions `SECURITY DEFINER` under the owner role.
Make the guard permit deletion only inside `audit_prune`.
This role separation requires a separate deployment change.
The default module does not implement it.

The trail records no reads or DDL changes.
Keep migration files in Git as the record of schema changes.
Events retain actor emails after an account is deleted.
This version has no command to pseudonymize or erase a person.
The owner must decide the legal basis for retaining personal data.
