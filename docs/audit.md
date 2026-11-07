---
title: Audit trail
parent: Build your app
nav_order: 11
---

# Audit trail

The audit trail answers "who changed this, and when?".
A database trigger writes one event for each row that changes.
Each event records the time, the actor, and the changed fields.
You get a History page for each audited record, an HTTP endpoint, and an `audit` command.
The module needs `auth` and the web app. It is off by default.

Turn it on for a new app:

```console
$ spinup new audited --module example.com/audited --modules auth,audit --no-git --non-interactive
...
$ cd audited
$ spinup g resource work_order title:string status:string --dry-run
...
$ grep -F 'auth.WithEvents(authsink.New(pool))' api/cmd/api/main.go
		auth.WithEvents(authsink.New(pool)),
```

Every new resource now has audit triggers.
Read the events from the command line:

<!-- doctest: skip -->
```console
$ cd api && go run ./cmd/api audit list --resource users
2026-11-01 18:23  unattributed     update #1  email: ada@example.com -> ada@example.org
2026-11-01 18:23  operator:you     create #1  email: ada@example.com, password_hash: changed (value hidden), role: manager
```

## What the trail records

- Every insert, update, and delete on an audited table, with the old and new values of each changed field.
- The actor: a signed-in user, an operator who ran a command, or a job. A change with no known actor is `unattributed`.
- Sign-in events, such as sign-in, sign-out, and password reset. See [Accounts and sign-in](auth.md).

It audits `users`, and `user_identities` when present. It redacts `password_hash`.
It does not audit `sessions`, `login_attempts`, or `password_resets`.
It records no reads and no schema changes. Keep your migration files in Git as the record of schema changes.

## Turn it on

### In a new app

Add `audit` to `--modules`, as the example above shows. Nothing else is needed.

### In an existing app

1. Run `spinup upgrade` so the app is current.
2. Run `spinup module add audit --yes --non-interactive`. The command adds `auth` too, if the app lacks it.
   It makes a branch, so inspect the branch before you merge it. Use `--dry-run` to see the plan first.
3. Read each warning in the plan. See [Migration numbers](#migration-numbers) for the number of the audit migration.
4. Edit `api/cmd/api/main.go` by hand. This file is yours, so the module cannot change it. The command prints these steps after `Next:`.

   ```go
   import (
       "github.com/stopyellingatme/spinup/kit/audit"
       "github.com/stopyellingatme/spinup/kit/audit/authsink"
   )

   // Add to the options of auth.New(pool, ...):
   auth.WithEvents(authsink.New(pool)),
   auth.WithActorContext(authsink.ActorContext),

   // Add to the options of app.New(...):
   app.WithCommand("audit", audit.Command(pool)),
   ```

5. Run `spinup db migrate up`, then restart the app.
6. Run `spinup doctor`. The `audit-wired` check confirms the wiring. It reads files only and needs no database.

The module adds the kit migration `api/db/kit/00006_audit_events.sql` and the component `history-list.svelte`.

{: .note }
If `audit-wired` warns that `user_identities` has no triggers, run `spinup g audit user_identities`. This happens only in an app whose OIDC migration came from an older spinup version.

### Migration numbers

The audit migration is a kit migration with the number 6. It lives in `api/db/kit/`.
The kit migrations have their own version numbers, so your own migration number 6 in `api/db/migrations/` does not clash with it.
The command `spinup db migrate up` applies the kit migration first.
Never rename an applied migration.

## Audit a resource

With the module on, `spinup g resource` adds audit triggers, a History endpoint, and a History page.

| Flag | Effect | Example |
| --- | --- | --- |
| `--no-audit` | Leave this resource out of the trail. | `spinup g resource note body:text --no-audit` |
| `--audit-redact` | Hide more columns. Event rows show `changed (value hidden)`. | `spinup g resource client ssn:string --audit-redact ssn` |
| `--history-role` | Give the History endpoint its own roles. | `spinup g resource work_order title:string --role staff --history-role manager` |

The generator also hides any column whose name contains `password`, `passwd`, `secret`, `token`, `api_key`, `apikey`, `hash`, or `credential`, in any case.
A later change to the hidden columns needs a new migration.

To audit a table that already exists, run `spinup g audit <resource>`.
It adds a migration, a History endpoint, and a History page.
It does not change your handlers or your detail page, so add a link to `/<plural-kebab>/{id}/history` on the detail page yourself.
Write changes with `audit.InTx` (see [Write audited changes](#write-audited-changes)).

{: .warning }
Never run `spinup g resource <name> --force` to add audit to an existing resource. It rewrites the create migration, and you may have applied that migration already.

This example needs an empty database in `DATABASE_URL`:

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

## See history on a page

The History page works without JavaScript.
It lists the time, the action, the actor email and role, and the field changes.
A hidden field shows `changed (value hidden)`. An empty record shows `No history yet.`
A deleted record has no detail page, so read its history with the API or the command line.

The same data is available as JSON:

<!-- doctest: skip -->
```console
$ curl -s -b cookies.txt 'http://localhost:8080/api/work_orders/7/history?limit=2'
```

```json
{
  "items": [
    {
      "id": 41,
      "occurred_at": "2026-11-01T18:23:56Z",
      "action": "update",
      "changes": {"status": {"old": "open", "new": "closed"}},
      "actor": {"kind": "user", "email": "ada@example.com", "role": "manager"}
    }
  ]
}
```

`limit` defaults to 50 with a maximum of 200. `offset` has a maximum of 10,000.
`before_id` pages by event number, which is better for long histories.
A larger value gives 422 with a `Try:` line. For larger queries, call `audit.List` with a cursor or a narrower range.
If you write a History route by hand, copy the generated role guard, owner filter, and limits before you call `audit.List`.
Only resources have an HTTP endpoint. Use the command line for auth events and other tables.

## Read events from the command line

The app binary reads `DATABASE_URL` and needs no web server.
It never prompts. A missing flag gives an error with a `Try:` line.

| Flag | Meaning | Example |
| --- | --- | --- |
| `--resource` | The table to read (required). | `--resource users` |
| `--id` | One record. | `--id 7` |
| `--actor` | The actor email. | `--actor ada@example.com` |
| `--action` | `create`, `update`, or `delete`. | `--action delete` |
| `--since`, `--until` | A date range. | `--since 2026-10-01` |
| `--unattributed` | Only changes with no actor. | `--unattributed` |
| `--limit` | Default 100, maximum 1000. | `--limit 20` |
| `--json` | One JSON object per line. | `--json` |

<!-- doctest: requires-env DATABASE_URL -->
```console
$ go run ./cmd/api audit list --resource users --limit 100 --json
...
$ go run ./cmd/api audit list --resource users --unattributed
...
```

An event in `--json` looks like this:

```json
{"id":2,"occurred_at":"2026-11-01T18:23:56.870977Z","action":"update","table_name":"users","record_id":"1","changes":{"email":{"new":"ada@example.org","old":"ada@example.com"}},"actor":{"kind":"unattributed","source":"sql"}}
```

## Write audited changes

Wrap each write in `audit.InTx` and use `s.Queries.WithTx(tx)`.
The auth middleware puts the signed-in user in the context.
The transaction binds that user as the actor before the data statement runs.

```go
err := audit.InTx(ctx, s.DB, func(tx pgx.Tx) error {
    _, err := s.Queries.WithTx(tx).UpdateWorkOrder(ctx, params)
    return err
})
```

A [background job](build/jobs.md) already runs with the actor `job` and the job kind.
Use `audit.WithActor` when you want a clearer reason for a batch:

```go
ctx = audit.WithActor(ctx, audit.System(audit.SourceJob, "close expired orders"))
err := audit.InTx(ctx, pool, func(tx pgx.Tx) error {
    _, err := tx.Exec(ctx, "UPDATE work_orders SET status = $1 WHERE status = $2", "closed", "expired")
    return err
})
```

- A write with no bound actor still records the change, as `unattributed`.
- Call `audit.Record` for an event that is not a row change, such as a download.
- If a row event fails, the data statement fails too, and the transaction rolls back.
- If a sign-in event fails, auth logs the error and the user continues.
- An update of N rows creates N events, so batch large updates and give a reason.

## Who can see history

History follows the same rules as reading one record of the resource.
An owned resource shows history only to its owner, whatever role the viewer has. No role bypasses this, including `admin`.
A `--shared` resource shows history to every signed-in user.
If the resource has `--role`, the viewer also needs one of those roles, or the answer is 403.
`--history-role` replaces the resource roles for the History endpoint only. By default, history uses the resource roles.

| Resource | Who sees history |
| --- | --- |
| Owned | The owner only. |
| Owned, with `--role R` | The owner, if the owner has role R. |
| `--shared` | Every signed-in user. |
| `--shared --role R` | Users with role R. |

The trail keeps the owner of each record, so deleting the record does not remove access to its past events.
A request with no matching events gives 200 with an empty list.
The response sets `Cache-Control: no-store`.

## Export and prune

The trail keeps events forever and never prunes by itself.
Export a range to a file with a SHA-256 digest you can keep:

<!-- doctest: requires-env DATABASE_URL -->
```console
$ go run ./cmd/api audit export --since 2000-01-01 --until 2100-01-01 --format jsonl --out audit.jsonl
...
$ test -f audit.jsonl
```

`--format` is `jsonl` or `csv`. The command prints the number of events and the SHA-256 digest of the file:

```text
exported 128 events
9f2c...e41a  audit.jsonl
```

The export reads pages of 1000 events and writes each page at once.
The trail does not record command reads.

{: .warning }
`audit prune` deletes every event before its cutoff, including row history. It has no filter by action. Export and review the range first.

Prune refuses a cutoff less than one day old.
It needs a reason and `--yes`. It writes an `audit.prune` event with the range, the count, and the reason.

<!-- doctest: requires-env DATABASE_URL -->
```console
$ go run ./cmd/api audit prune --before 2000-01-01 --reason 'retention policy review' --yes
...
```

Plan for about 1 KB per event, with indexes. At 10,000 events a day, that is about 3.6 GB a year.
Backups include the trail, because it lives in the app database.
Keep row history unless your retention policy requires deletion. Sign-in events are a good candidate for an age limit.

## Limits and hardening

{: .important }
The trail is not tamper-proof. The default app database user owns the tables, so it can disable the triggers, call the prune function, or edit events.

The guard trigger stops accidental updates, deletes, and truncates of `audit_events`.
It does not stop code that runs as the table owner.
There is no hash chain and no external anchor.
Events keep actor emails after an account is deleted, and this version has no command to erase a person.
Decide the legal basis for keeping personal data before you turn the module on.

For stronger protection, run migrations as a separate database role that owns the tables and functions.
Give the app role only `INSERT` and `SELECT` on `audit_events`.
Make the prune and trigger functions `SECURITY DEFINER` under the owner role.
This needs a change to your deploy. The default module does not do it.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `audit list: no events match` | No event fits the filters, or the table has no triggers. | Remove a filter. Run `spinup doctor`. |
| Events show `unattributed` | The write did not go through `audit.InTx`, or `auth.WithActorContext` is missing. | Wrap the write in `audit.InTx`. Check `main.go`. |
| `audit-wired` warns | A piece of the wiring in `main.go` is missing. | Add the lines from [In an existing app](#in-an-existing-app). |
| History gives 403 | The user lacks a required role or does not own the record. | Check `--role` and `--history-role`. |

## See also

- [Generate audit history](commands/generate-audit.md) and the [flag reference](site/commands/generate-audit.md)
- [Accounts and sign-in](auth.md) for the auth events
- [Roles](build/roles.md)
- [Background jobs](build/jobs.md)
- Design record: [ADR 0025](https://github.com/stopyellingatme/spinup/blob/main/docs/adr/0025-audit-trail.md)
