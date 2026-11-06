---
title: generate audit — track changes to a table
parent: Command guides
---

# generate audit — track changes to a table

An audit trail records who changed which row, and when.
`spinup g audit <resource-or-table>` adds the trail to a table that already exists.
New resources get it by default, so you use this command for older tables.

The app needs the `audit` module.
The command writes a new migration with database triggers, and the files for a history endpoint and page.
It keeps your existing handlers and pages as they are.

```console
$ spinup new crm --module example.com/crm --modules ci,auth,audit --no-git --non-interactive
...
$ cd crm
$ spinup g resource work_order title:string --no-audit --api-only
...
$ spinup g resource part name:string --no-audit --api-only
...
$ spinup g audit work_order --api-only --dry-run
create api/db/migrations/00004_audit_work_orders.sql (seed)
create api/internal/handlers/work_orders_history.go (scaffold)
create api/openapi/paths/work_orders_history.yaml (scaffold)
create api/openapi/schemas/audit_event.yaml (scaffold)
$ spinup g audit work_order --api-only
Created 4, updated 0 files
Next: spinup db migrate up
Next: wrap Create, Update, and Delete writes in audit.InTx(ctx, s.DB, func(tx pgx.Tx) error { /* use s.Queries.WithTx(tx) */ return nil })
Next: add a History button that links to /work-orders/{id}/history on the detail page
```

Without `--api-only`, the command also writes the history page in `web/`.
The three `Next:` lines are your to-do list: apply the migration, wrap the writes, and link the page.

## What it writes

| File | What it is |
| --- | --- |
| `api/db/migrations/NNNNN_audit_<table>.sql` | Triggers that record every insert, update, delete, and truncate. Apply it with `spinup db migrate up`. |
| `api/internal/handlers/<plural>_history.go` | The handler of the history route. |
| `api/openapi/paths/<plural>_history.yaml` | The route `GET /api/<plural>/{id}/history`. |
| `api/openapi/schemas/audit_event.yaml` | The event schema. The first audited resource writes it. |
| `web/src/routes/<plural-kebab>/[id]/history/` | The history page, in kebab case such as `work-orders`. Skipped with `--api-only`. |

The command stops if the table already has an audit trigger.
You can pass a resource name (`work_order`) or a table name (`work_orders`).

## Wire the writes

The trigger records the change.
To record the actor too, wrap each create, update, and delete in `audit.InTx`.
Use `s.Queries.WithTx(tx)` inside the function:

```go
var row store.WorkOrder
err := audit.InTx(ctx, s.DB, func(tx pgx.Tx) error {
	var err error
	row, err = s.Queries.WithTx(tx).UpdateWorkOrder(ctx, params)
	return err
})
```

Without an actor, the trigger records the change as `unattributed`.
Run the migration before you start the new binary.
Then add a History button on the detail page that links to `/work-orders/{id}/history`.

## Hide sensitive columns

Use `--audit-redact column_a,column_b` to hide the values of more columns.
The trail already hides columns whose names contain `password`, `passwd`, `secret`, `token`, `api_key`, `apikey`, `hash`, or `credential`.
A hidden change shows `{"redacted": true}` instead of the value.

```console
$ spinup g audit part --audit-redact name --api-only --dry-run --diff | grep 'CREATE TRIGGER parts_audit '
+CREATE TRIGGER parts_audit AFTER INSERT OR UPDATE OR DELETE ON parts FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id', 'name');
```

The trigger fixes the list when the migration runs.
To change the list later, write a new migration.

## Limit who reads the history

Use `--history-role manager,staff` to let only these roles read the history.
The roles must be declared in `[auth]`.
By default, the history uses the roles of the generated `Get` handler.
A role never bypasses the owner filter: a user sees the history of their own rows only.

```console
$ spinup g audit part --history-role manager --api-only --dry-run --diff | grep HasRole
+	if !user.HasRole(roles.Manager) {
```

## Read the history

The history route returns the events of one record, newest first.
It still returns the events after the record is deleted.

```sh
curl 'http://localhost:8080/api/work_orders/7/history?limit=20'
```

```json
{
  "items": [
    {
      "id": 41,
      "occurred_at": "2026-11-01T09:30:00Z",
      "action": "update",
      "table_name": "work_orders",
      "record_id": "7",
      "changes": { "title": { "old": "Fix pump", "new": "Replace pump" } },
      "actor": { "kind": "user", "id": 3, "email": "ann@example.com", "role": "manager" }
    }
  ]
}
```

| Query parameter | Default | Limit |
| --- | --- | --- |
| `limit` | 50 | 200 |
| `offset` | 0 | 10000 |
| `before_id` | none | Returns events older than this event id. Use it to read the next page. |

An invalid value gets a 422 problem response with a `Try:` line.
An empty result is an empty list.
The response has `Cache-Control: no-store`.

## Audit the identity table

If you use [sign-in with an identity provider](../build/oidc.md), the table `user_identities` can have a trail too.
A new app needs no command, because its OIDC migration attaches the trigger when the audit module is on.
An older app has a migration without it.
For that app, run `spinup g audit user_identities`.
It writes only a migration, and it is safe in either migration order.
The doctor check `audit-wired` tells you when you need it.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `a migration already attaches its audit trigger` | The table has a trail. Nothing to do. |
| `no migration creates the table` | Check the name. Pass a resource or a table that a migration creates. |
| The actor shows as `unattributed` | The write is not in `audit.InTx`, or the auth setup lacks the actor option. See [Audit trail](../audit.md). |
| `cannot use history role "x"` | Declare the role in `[auth]` and run `spinup g roles`. |

## See also

- [Audit trail](../audit.md): the full guide to the trail
- [generate resource](generate-resource.md): new resources include the trail
- [generate roles](generate-roles.md)
- Flag reference: [`generate audit`](../site/commands/generate-audit.md)
- Design record: [audit trail](https://github.com/stopyellingatme/spinup/blob/main/docs/adr/0025-audit-trail.md)
