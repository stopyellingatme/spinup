# Add audit to an existing table

Run `spinup g audit <resource-or-table>` after you enable the audit module.
The command writes a new migration that attaches the row and truncate triggers.
It refuses a table that already has an audit trigger.

For a resource, the command adds separate files for the history operation, handler, and web route.
It preserves the existing handlers and pages.
Use `--api-only` to skip the history web route.
For an auth table, the command writes only the migration.
The key of `user_identities` is `provider,subject`.
The OIDC migration `00004_oidc_identities.sql` attaches its own audit trigger when the audit module is on, so a new app needs no command for `user_identities`.
An app whose `00004_oidc_identities.sql` came from an older spinup version has no such block.
For that app, `spinup g audit user_identities` writes an attach that first drops the trigger if it exists, so it is safe in either migration order.
The doctor check `audit-wired` names this command when it is needed.
The owner argument for this table is empty.

```console
$ spinup g audit --help
...
```

Use `--audit-redact column_a,column_b` to hide more values.
The generator also hides values from columns with sensitive names.
These names match `password`, `passwd`, `secret`, `token`, `api_key`, `apikey`, `hash`, or `credential`.
A redacted change shows `{"redacted": true}`.
The trigger fixes this list when the migration runs.
To change the list later, write a new migration.

Use `--history-role manager,staff` to restrict history to declared roles.
The history roles default to the roles in the generated Get handler.
The history endpoint uses the same owner filter as Get.
A role does not bypass the owner filter.

The command prints the next actions.
Run the migration before you start the new binary.
Wrap each Create, Update, and Delete write in `audit.InTx`.
Use `s.Queries.WithTx(tx)` inside the transaction.
Add a History button that links to `/<plural>/{id}/history` on the detail page.

```go
var row store.WorkOrder
err := audit.InTx(ctx, s.DB, func(tx pgx.Tx) error {
    var err error
    row, err = s.Queries.WithTx(tx).UpdateWorkOrder(ctx, params)
    return err
})
```

The history endpoint returns events after the record is deleted.
It returns an empty list when the caller has no matching events.
It sets `Cache-Control: no-store`.
The default limit is 50 events.
The maximum limit is 200 events.
The maximum offset is 10000 events.
Use `before_id` for the next page of older events.
An invalid limit or offset receives a 422 problem response with a `Try:` line.

An actor needs the `authsink.ActorContext` option in the auth configuration.
Without an actor, the trigger records the change as `unattributed`.
See [the audit contract](../adr/0025-audit-trail.md) for the wiring and the limits of the trail.
