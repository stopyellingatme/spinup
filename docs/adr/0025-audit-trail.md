# ADR 0025: Audit trail

Status: accepted
Date: 2026-10-31
Extends: ADR 0013 section 1 (modules), ADR 0015 section 1 (ownership), ADR 0024 (roles)

## Context

The first customer may show change records to an auditor or an insurer.
The record must answer one question: who changed this record, and when.
Today a generated app keeps only `created_at` and `updated_at`. It keeps no old values and no actor.
The generated handlers call `s.Queries` one statement at a time. They use no transaction.
The auth module records no sign-in event. The `user set-role` command leaves no trace.

People read the trail in two places.
The first place is a History page for each record in the web app.
The second place is an operator command on the app binary, in the style of `user` (`kit/auth/usercmd.go`).

This ADR adds a module `audit`, a kit package `kit/audit`, a retrofit generator, a change to the resource generator, and one web component.

## Decisions

### 1. Promise and limits

1. The trail is complete for row changes. A database trigger writes one event for each insert, update, delete, and truncate of an audited table.
   This covers generated handlers, handwritten handlers, jobs, the operator commands, and `psql`.
2. The trail is attributed when the code binds an actor (decision 4). A write with no actor gets the actor kind `unattributed`. The trigger never drops it.
3. The trail fails closed for data. If the trigger cannot write the event, the data change fails and rolls back.
4. The trail fails open for sign-in, sign-out, and lockout events. If the write fails, the code logs the error and the user can still sign in.
   A fresh binary on an old schema must not lock out every user.
5. A guard trigger rejects a mistaken `UPDATE`, `DELETE`, or `TRUNCATE` on the event table (decision 8).
   The guard stops accidental statements only. It does not stop application code, a handwritten handler, or SQL injection.
   The application user owns the table and can set the prune setting, call `audit_prune`, or disable the trigger.
6. The trail is not tamper-proof against the application or against the database owner. Decision 8 states this limit and the hardening step.
7. A hash chain for tamper evidence is out of scope. A later ADR may add it.
8. Read access follows the access to the record (decision 6).

### 2. Module and layout

1. Add the module `audit`. It is the pack `internal/templates/packs/audit` with `requires = ["auth"]`.
   A pack becomes a module with no registration (ADR 0013 section 1). `internal/templates` embeds every pack directory, and the catalog in `internal/modules` lists each pack except `base`, `api`, `web`, and the names that start with `_`.
   `spinup module list` and `spinup module add` read this catalog. `spinup module add audit` adds `auth` too, because `Select` adds the required packs recursively.
   The audit module needs the web frontend through `auth` (`requires = ["web"]`). `spinup new --api-only` hides it, and `--api-only --modules audit` fails with `needs the web frontend`.
   The pre-seed made `selectPacks` in `internal/gen/newapp` follow the requirement chain for this check. Before, it read only the direct requirements of a pack.
   The module has no `[[questions]]`, so it adds no flag to `spinup new` or `spinup module add`.
2. The audit module is a separate module and not part of the auth pack. Audit also covers tables that have no link to auth.
3. The kit package `kit/audit` holds the Go API (decision 4). The pack holds the schema, one web component, and the wiring.
4. The pack files are:
   - `api/db/migrations/00006_audit_events.sql`, class `seed`. It holds the schema of decision 3.
   - `web/src/lib/components/app/history-list.svelte`, class `managed`. Unit P adds it. The pre-seed pack holds only the migration.
5. Facts about the auth migrations:
   - The auth pack always ships `00002_create_auth.sql` and `00005_users_role.sql`.
   - It ships `00003_password_resets.sql` only with the mail module (`pack.d/reset.toml`).
   - It ships `00004_oidc_identities.sql` only with `auth_oidc` (`pack.d/oidc.toml`).
   - The jobs pack and the mail pack ship no other migration. The audit pack takes the number 00006.
   - The audit migration needs only the table `users` (from 00002). It does not need `users.role`, `user_identities`, or `password_resets`.
6. The kit migrator accepts out-of-order versions (ADR 0024). It does not accept two files with the same version.
   `spinup module add audit` and `spinup upgrade` check every pack migration they add against the migration files of the app.
   If a file has the same number, the command prints a warning with a `Try:` line. The line tells the owner to rename the app file to the next free number.
   The check covers any number from 00003 to 00006, because `g resource` uses the next free number (for example, `00003_create_todos.sql` in the todo app).
7. The module is off by default. `spinup module add audit` turns it on in an existing app.
8. The `api` pack `main.go.tmpl` gets lines under `has "audit" .modules` (decision 4.8).
9. An app without the module renders byte-identical files. The golden trees of those apps do not change.

### 3. Schema

The table is `audit_events`.

| Column | Type | Meaning |
|---|---|---|
| `id` | `bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY` | Order key |
| `occurred_at` | `timestamptz NOT NULL DEFAULT clock_timestamp()` | Database clock. The app never sets it. |
| `action` | `text NOT NULL`, check `^[a-z][a-z0-9_.]{0,63}$` (constraint `audit_events_action_check`) | `create`, `update`, `delete`, `truncate`, or an event name such as `auth.sign_in` |
| `table_name` | `text NOT NULL` | The subject table. Auth events use `users`. |
| `record_id` | `text NOT NULL` | The key of the subject row (decision 3.5). The value `*` means the whole table. |
| `owner_id` | `bigint` | The value of the owner column of the row. No foreign key. |
| `changes` | `jsonb NOT NULL DEFAULT '{}'` | The field changes (decision 3.3) |
| `actor_kind` | `text NOT NULL`, check in `user`, `operator`, `system`, `unattributed` (constraint `audit_events_actor_kind_check`) | |
| `actor_id`, `actor_email`, `actor_role` | `bigint`, `text`, `text` | A snapshot at the time of the event. No foreign key. |
| `source` | `text NOT NULL` | `api`, `cli`, `job`, `migrate`, or `sql`. No check constraint: an unknown source must not stop a data write. |
| `reason` | `text` | Free text from the operator or the code |
| `request_id` | `text` | From the request context |

1. Indexes: `audit_events_record_idx (table_name, record_id, id DESC)`, `audit_events_owner_idx (owner_id, table_name, record_id, id DESC)`, `audit_events_actor_idx (actor_id, id DESC) WHERE actor_id IS NOT NULL`, and `audit_events_occurred_at_idx (occurred_at)`.
2. A snapshot column has no foreign key. A deleted user must not erase or block history.
3. The `changes` format:
   - insert: `{"col": {"new": v}}` for each non-null column
   - update: `{"col": {"old": v, "new": v}}` for each column that changed (`IS DISTINCT FROM` on the `jsonb` values)
   - delete: `{"col": {"old": v}}` for each column, null columns included. This is a full snapshot of the deleted row.
   - redacted column: `{"col": {"redacted": true}}`. An insert lists it when it is not null. An update lists it only when it changed. A delete lists it always, as part of the snapshot.
   - value larger than 4096 bytes: `{"truncated": true, "bytes": N, "sha256": "<hex>"}` in place of the value.
     `N` and the digest use the text of the value (`v #>> '{}'`, so a JSON string has no quotes) in UTF-8. The helper `audit_capped(jsonb)` does this.
   - The columns `id`, `created_at`, and `updated_at` are never listed.
4. An update that changes no listed column writes no event.
5. The key of a row (`record_id`):
   - The trigger takes the key columns as an argument. An empty argument means `id`.
   - A table with a composite key lists all key columns. The trigger reads each value from `to_jsonb(NEW)` (`to_jsonb(OLD)` for a delete).
   - `record_id` is the key values joined with `:`. Example: `google:1234567` for the key `(provider, subject)`. The pre-seed proved this on `user_identities`.
   - `record_id` is never NULL. A NULL key value becomes an empty string.
   - PostgreSQL does not run a trigger function at `CREATE TRIGGER`, so no check can run at creation time.
     The trigger function checks each key column and the owner column when a row passes. A missing column raises `audit: the key column X is not a column of T` with a `Try:` hint, so the data write fails and rolls back.
     The generator checks the key and owner columns against the resource spec before it writes the attach statement. The migration tests insert a row through each attach statement.
6. The migration is a goose file. It uses `-- +goose Up` and `-- +goose Down`.
   Each function body that holds a semicolon sits between `-- +goose StatementBegin` and `-- +goose StatementEnd`. Without the markers goose splits the body wrongly.
7. The migration creates these functions in the `public` schema.
   Each function sets `SET search_path = pg_catalog, public, pg_temp`.
   Each function is `SECURITY INVOKER`. The trigger functions run with the rights of the caller, and the caller needs `INSERT` on `audit_events`.
   - `audit_append(action text, table_name text, record_id text, owner_id bigint, changes jsonb, actor_kind text, actor_id bigint, actor_email text, actor_role text, source text, reason text, request_id text) RETURNS bigint`
     is the one writer (`LANGUAGE sql`). It inserts the row from explicit parameters and returns the new `id`. It reads no setting.
     A NULL `changes` becomes `{}`. An empty or NULL `actor_kind` becomes `unattributed`. An empty or NULL `source` becomes `sql`. An empty `actor_email`, `actor_role`, `reason`, or `request_id` becomes NULL.
   - `audit_actor() RETURNS jsonb` reads the setting `spinup.audit`. An unset or empty setting gives `{}`. A value that is not a JSON object raises an error, so the data write fails.
   - `audit_capped(v jsonb) RETURNS jsonb` replaces a value larger than 4096 bytes (decision 3.3).
   - `audit_row_change()` is the row trigger function. It calls `audit_actor()`, builds `changes` from `OLD` and `NEW`, and calls `audit_append`.
     The trigger arguments are: the owner column (an empty string for none), the key columns separated by commas (an empty string for `id`), then one argument for each redacted column.
     The owner column value is cast to `bigint`. An owner column of another type fails the write.
     The action is `create` for an insert, `update`, or `delete`.
   - `audit_truncate()` is the statement trigger function for `TRUNCATE`.
   - `audit_immutable()` is the guard of decision 8.
   - `audit_prune(before timestamptz, reason text) RETURNS bigint` is the one delete path (decision 10).
8. The generator attaches a table with these statements:
   `CREATE TRIGGER <table>_audit AFTER INSERT OR UPDATE OR DELETE ON <table> FOR EACH ROW EXECUTE FUNCTION audit_row_change('<owner column>', '<key columns>', '<redacted>', ...);`
   `CREATE TRIGGER <table>_audit_truncate AFTER TRUNCATE ON <table> FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();`
   The Down step is `DROP TRIGGER IF EXISTS <table>_audit_truncate ON <table>;` and `DROP TRIGGER IF EXISTS <table>_audit ON <table>;`.
   For a generated resource, the owner column is `user_id` for an owned resource and `''` for a shared resource (ADR 0015). The key is `id`.
   Example for an owned resource with one redacted column: `audit_row_change('user_id', 'id', 'api_token')`.
9. The module attaches triggers to `users`: `audit_row_change('', 'id', 'password_hash')`.
   It attaches a trigger to `user_identities` only when the table exists: `audit_row_change('', 'provider,subject')`. The owner is empty and no column is redacted. `spinup g audit user_identities` writes the same arguments.
   Each attach statement sits in a `DO` block that checks `to_regclass('public.<table>') IS NOT NULL`. The block skips a missing table without an error.
   The todo app has no `user_identities` table, so the migration skips it there.
   If the OIDC migration runs after the audit migration, `user_identities` has no trigger. The command `spinup g audit user_identities` attaches it.
   The doctor check `audit-wired` warns when `user_identities` exists and has no trigger.
   The module does not attach triggers to `sessions`, `login_attempts`, or `password_resets`. These tables hold token hashes and have no meaning for an auditor.
10. The Down step drops the triggers with `DROP TRIGGER IF EXISTS ... ON <table>`, then the table, then the functions, without `CASCADE`.
    `DROP TRIGGER IF EXISTS` on a table that does not exist gives a notice, not an error (proved on 16, 17, and 18), so the Down step runs on the todo shape.
    A later migration that attached a trigger with `audit_row_change` must roll back first. Goose rolls back newest first, so this holds.
11. The design runs on Postgres 16, 17, and 18 (`stackversions.yaml`). The triggers use `OLD` and `NEW`.
    The design does not use `RETURNING OLD`, because only Postgres 18 has it.
12. The generated sqlc schema reads `db/migrations`. The pre-seed proved that sqlc v1.31.1 (the pinned tool of the api pack) parses the migration with its PL/pgSQL bodies and `DO` blocks.
    sqlc makes the model `store.AuditEvent` (`Changes []byte`, nullable columns as `pgtype.Int8` and `pgtype.Text`) and accepts queries that read `audit_events` and call `audit_append`.

### 4. How records are written

1. Row events come from the triggers. Handlers do not build change lists.
2. The actor context for a row trigger is the transaction-local setting `spinup.audit`. Its value is one JSON object:
   `{"kind","id","email","role","source","reason","request_id"}`. Every key is optional.
   If the setting is empty, the event gets `actor_kind = 'unattributed'` and `source = 'sql'`.
3. The package `kit/audit` has this API. It does not import `kit/auth`, and `kit/auth` does not import `kit/audit`.
   - `type Execer interface { Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) }`. A pool, a `pgx.Conn`, and a `pgx.Tx` satisfy it.
   - `type Querier interface { Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) }`, for `List`.
   - `type Actor struct { Kind, Email, Role, Source, Reason, RequestID string; ID int64 }`. Its JSON tags are the keys of decision 4.2 with `omitempty`. A zero `ID` means no user ID.
   - The constants `KindUser`, `KindOperator`, `KindSystem`, `KindUnattributed`, and `SourceAPI`, `SourceCLI`, `SourceJob`, `SourceMigrate`, `SourceSQL`.
   - `type Event struct { ID int64; OccurredAt time.Time; Action, Table, RecordID string; OwnerID int64; Changes json.RawMessage; Actor Actor }`. A zero `OwnerID` means no owner.
   - `type Filter struct { Table, RecordID string; OwnerID int64; ActorEmail, Action string; Since, Until time.Time; Unattributed bool; BeforeID int64; Limit, Offset int }`.
     `BeforeID` is the cursor (events with a lower `id`). The constants are `DefaultLimit` (50), `MaxLimit` (200), and `MaxOffset` (10000).
     `Filter.Validate()` returns an error that wraps `ErrFilterLimit` and ends with a `Try:` line.
   - `WithActor(ctx, Actor) context.Context` and `ActorFrom(ctx) (Actor, bool)`
   - `Operator() Actor` and `System(source, reason string) Actor`
   - `Bind(ctx context.Context, tx Execer) error`. It runs `SELECT set_config('spinup.audit', $1, true)`.
     The third argument `true` makes the setting local to the transaction. A pooled connection never keeps it.
     `Bind` returns `ErrNotInTx` when `tx` is a `*pgxpool.Pool` or a `*pgx.Conn`. A pool runs the statement in its own transaction, so the setting would be lost.
     The auth sink and `InTx` pass a transaction, so both reach `Bind` with an `Execer` that is a `pgx.Tx`.
   - `InTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error`. It calls `db.InTx`, then `Bind`, then `fn`.
     The actor comes from `ActorFrom`. A middleware stores it (decision 4.5).
   - `Record(ctx context.Context, q Execer, e Event) error`. It records an event that is not a row change.
     It takes the actor from the `Event` or from `ActorFrom(ctx)` and passes the actor as explicit parameters to `audit_append`.
     It reads no setting, so `q` can be a pool or a transaction.
   - `List(ctx context.Context, q Querier, f Filter) ([]Event, error)`. `Filter` has a limit and a cursor (decision 6.7). It returns the events newest first.
   - `Prune(ctx context.Context, pool *pgxpool.Pool, before time.Time, reason string) (int64, error)`
   - The subpackage `kit/audit/authsink` holds `New(pool *pgxpool.Pool) auth.EventSink` and `ActorContext(ctx context.Context, u auth.User) context.Context`, so `kit/audit` stays free of `kit/auth`.
   - `Command(pool *pgxpool.Pool) func(ctx context.Context, args []string) error`
4. `kit/auth` defines `type Event struct { Name string; User User; Actor string; Method string; Reason string }` (file `kit/auth/events.go`).
   The event names are the constants `EventRegister`, `EventUserCreate`, `EventRoleChange`, `EventPasswordReset`, `EventPasswordRehash`, `EventSignIn`, `EventSignOut`, and `EventLockout`.
   `Event.Actor` is the operator email for the operator command. `Event.Method` is `password` or `oidc` for a sign-in.
   It defines `type EventSink func(ctx context.Context, q Execer, e Event) error`, a local `Execer` with the same method set as `audit.Execer`, and the option `auth.WithEvents(s EventSink)`.
   The sink in `authsink` converts the `auth.Execer` to an `audit.Execer` without a cast, because the method sets are equal.
   Without the option, `kit/auth` records no explicit event. The triggers still record row changes.
   The options store the sink and the actor function on the `Auth` struct (fields `events` and `actorContext`). Nothing calls them before unit K.
5. The auth middleware puts an `audit` actor in the context through a second option, `auth.WithActorContext(func(ctx, User) context.Context)`.
   The `authsink` package gives the function that calls `audit.WithActor`. Handlers then reach `audit.InTx` with the signed-in actor.
6. The auth package calls the sink at these points:
   - `register`, `user_create`, `role_change`, `password_reset`, and `password_rehash`. These are row changes of `users`.
     Auth calls the sink inside the transaction of the change, with the transaction as `q`. The sink only binds the actor with `Bind`. If the sink fails, the change fails.
     The registration actor is the new user, as `kind = system`, `reason = register`, because the ID does not exist before the insert.
   - `sign_in` (method `password` or `oidc`), `sign_out`, and `lockout`. The sink inserts an explicit event with `Record`.
     Auth calls the sink after the effect. If the sink fails, auth logs the error and continues (decision 1.4).
   - `lockout` is written once for each lockout, for an existing account only. A failed login for an unknown email writes nothing.
   - The first OIDC identity insert writes a row event for `user_identities` with a composite `record_id`. Registration through OIDC must not fail because of the trigger (decision 3.5).
   - The lane wraps a write in `db.InTx` when the current code does not use a transaction for it.
7. The operator actor has `kind = operator`, `source = cli`, and the email `operator:<login>`.
   The login is `SUDO_USER`, then `USER`, then `unknown`.
   The commands `user create` and `user set-role` get an optional `--reason` flag.
8. The lines in `main.go.tmpl` under `has "audit" .modules`:
   - `auth.WithEvents(authsink.New(pool))` and `auth.WithActorContext(authsink.ActorContext)` in the `auth.New` call
   - `app.WithCommand("audit", audit.Command(pool))`
9. The jobs worker puts `audit.WithActor(ctx, audit.System("job", "<job kind>"))` into the job context before each job. It does not call `Bind`.
   River jobs use the pool and not a transaction, so a `Bind` call has no effect.
   Only a write through `audit.InTx` with this context gets the actor. This applies only to apps with the jobs module.
10. Migrations bind an actor in the SQL of the migration, not in the migrator.
    A `set_config(..., true)` on a separate statement or connection cannot reach a goose transaction.
    A data migration that needs attribution starts with
    `SELECT set_config('spinup.audit', '{"kind":"system","source":"migrate"}', true);`
    A migration without this line gets `unattributed` events with the source `sql`. The ADR accepts this.
    The kit migrator does not change. The pre-seed proved that the line reaches the trigger inside a goose transaction, through `db.NewMigrator`, on Postgres 16, 17, and 18 (`kit/audit/schema_test.go`, migration `00008_migrate_actor.sql`).
    The next migration without the line writes `unattributed` events with the source `sql`. The setting does not leak from one migration to the next.
11. Redaction rules:
    - A column is redacted when its name matches `(?i)(password|passwd|secret|token|api_?key|hash|credential)`.
    - The flag `--audit-redact a,b` adds more columns.
    - A redacted column records `{"redacted": true}` and records it only when the value changes.
    - The list is fixed when the trigger is created. To change it, write a new migration.
12. An optional export event exists. A handwritten handler for a report or an export calls `audit.Record` with the action `report.render` or `export`.

### 5. Generators

1. If the audit module is on, `spinup g resource` audits the new table by default. The flag `--no-audit` turns it off for one resource.
   If the module is off, `--audit` fails. `Try: spinup module add audit`.
2. New flags: `--audit-redact <cols>` and `--history-role <roles>`.
   The history roles default to the roles of the resource. They must be a subset of the declared roles (ADR 0024).
3. The `Spec` struct gets the field `Audit AuditSpec { On bool; Redact []string; HistoryRoles []string }`.
4. The create migration of the resource gets the trigger statements after `CREATE TABLE`.
   The generator checks that a migration with `audit_events` exists and has a lower number than the new migration. If not, it fails.
   `Try: spinup module add audit, then run the generator again.`
5. The `Create`, `Update`, and `Delete` handlers run their query in `audit.InTx`:
   ```go
   var row store.WorkOrder
   err := audit.InTx(ctx, s.DB, func(tx pgx.Tx) error {
       var err error
       row, err = s.Queries.WithTx(tx).UpdateWorkOrder(ctx, params)
       return err
   })
   ```
   The `List` and `Get` handlers do not change. The pre-seed proved that sqlc v1.31.1 generates `func (q *Queries) WithTx(tx pgx.Tx) *Queries` in `internal/store/db.go` for `sql_package: pgx/v5`.
6. The generator adds the history operation:
   - the OpenAPI path `GET /api/<plural>/{id}/history` with `limit` and `offset` (maximum values in decision 6.7). The operation ID is `List<Singular>History`.
   - the shared schemas `AuditEvent` and `AuditEventList` in `api/openapi/schemas/audit_event.yaml`, class `scaffold`, written once
   - the handler in the new file `api/internal/handlers/<plural>_history.go`, class `scaffold`
7. The generated tests create, update, and delete one row. They assert three events with the actor and the `old` and `new` values.
   They assert a 403 for a role outside the history roles and an empty list for another owner.
   They assert a 422 for a `limit` or `offset` over the maximum.
8. `spinup g audit <resource-or-table>` is the retrofit for a table that exists. It writes:
   - a new migration `NNNNN_audit_<table>.sql` that attaches the triggers. The Down step drops them. `NNNNN` is the next free number.
   - for a resource: the history OpenAPI path, the history handler file, and the history web route
   It edits no handler and no page. Its `Next:` lines print the `audit.InTx` recipe (decision 9) and the History button.
   It refuses a table that already has the trigger.
   For an auth table such as `user_identities`, it writes the migration only, with the key and redact arguments of decision 3.9.
9. Never run `spinup g resource <name> --force` on an existing resource to add audit. The force path rewrites the applied create migration.
   The docs say so. Use `spinup g audit`.

### 6. Who can read a history

1. The history endpoint has the same guard as the Get operation of the resource: a signed-in user, then the history roles (ADR 0024), then the owner filter (ADR 0015).
2. The owner rule for each role is the rule of the Get operation of the same resource. No role sees more history than it sees records.
   - For an owned resource, a role that Get filters by owner gets `owner_id = user.ID`.
   - If ADR 0015 or ADR 0024 lets a role bypass the owner filter on Get (for example `admin`), that role bypasses it on history. No other role does.
   - For a shared resource, no owner filter applies.
   The pre-seed read ADR 0015, ADR 0024, and the resource generator. The Get guard of a generated resource (`internal/gen/resource/handlers.go`, the `guard` closure and the Get handler) is:

   | Resource | Sign-in | Role check | Owner filter on Get | Owner filter on history |
   |---|---|---|---|---|
   | Owned (the default with auth), no `--role` | yes (`auth.Required`, then 401 in the handler) | none | `WHERE id = $1 AND user_id = $2` | `owner_id = user.ID` |
   | Owned, `--role R` | yes | `user.HasRole(R...)`, else 403 | the same `user_id` filter for every role | `owner_id = user.ID` for every role |
   | `--shared`, no `--role` | yes | none | none | none |
   | `--shared --role R` | yes | `user.HasRole(R...)`, else 403 | none | none |
   | No auth module | none | none | none | not possible: the audit module requires auth |

   No role bypasses the owner filter. `HasRole` is an exact match and ignores `Roles.Admin` (`kit/auth/roles.go`). ADR 0024 decision 8.5 says that a role does not change the `user_id` filter.
   The owner column of a generated resource is `user_id`, and the trigger stores its value in `owner_id`.
   For history, the history roles take the place of the resource roles in the role check. Unit P copies this table into `docs/audit.md`.
3. The filter uses the stored `owner_id`. The owner can still read the history of a deleted record through the API.
4. A caller with no matching events gets 200 and an empty list. The endpoint never answers 404 for a missing record. It does not reveal that a record exists.
5. The response lists the time, the action, the actor (ID, email, role, kind), the request ID, and the changes.
6. The response sets `Cache-Control: no-store`.
7. Page size limits:
   - `limit` defaults to 50 and has the maximum 200. `offset` has the maximum 10,000.
   - A value over the maximum answers 422 as `application/problem+json` with a `Try:` line. The line names the cursor or a narrower range.
   - `audit.Filter` has the same maximums. The handler never builds an unbounded query.
8. Events of other tables (sign-in, role changes) have no API in this version. Only the operator command reads them.

### 7. Web

1. The detail page of a resource gets a `History` button. It links to `/<plural-kebab>/{id}/history`.
2. The history page is server-rendered. Its load function calls the typed client and uses `requireRole` with the history roles. It needs no JavaScript and it prints well.
3. The component `history-list.svelte` shows the time, the actor email and role, and each change as `field: old -> new`.
   It shows `changed (value hidden)` for a redacted field. It has an empty state and uses the existing pagination component if the web pack has one.
   The lane checks the helper names in the web pack.
4. Example of the page:
   ```
   Work order #42                [History]
     2026-11-03 09:12  ana (manager)  status: open -> assigned
     2026-11-03 14:40  ben (staff)    status: assigned -> done
   ```
5. If the history call fails, the detail page still loads. The History page shows the error.
6. `checkPageInputs` lists `history-list.svelte` as a required file when the resource is audited.
7. A deleted record has no detail page. Read its history with the command.
8. Page files are `scaffold`. An old page keeps its code until the owner runs `spinup g audit`.

### 8. Tamper resistance

1. A guard trigger on `audit_events` rejects `UPDATE`, `DELETE`, and `TRUNCATE`.
   `audit_prune` sets the transaction-local setting `spinup.audit_prune` to `on`. The guard allows a delete only when this setting is on.
2. The limit: the guard stops accidental statements and mistaken code. It does not stop hostile or buggy application code.
   Any code that uses the application database user can run `set_config('spinup.audit_prune','on',true)` and then `DELETE FROM audit_events`.
   That user also owns the tables in this version, so it can call `audit_prune`, disable the trigger, and edit rows.
   The docs state this limit in plain words. They do not call the trail append-only for the application.
3. The hardening step: use a separate migration role that owns the tables and the functions, and an app role with `INSERT` and `SELECT` on `audit_events` only.
   Then make `audit_prune` and the trigger functions `SECURITY DEFINER` under the owner role, and make the guard allow a delete only when it runs inside `audit_prune`.
   This hardening is a documented step and not the default.
4. A hash chain and external anchoring are a later ADR.
5. Backups include the table, because it lives in the app database.

### 9. Operator command and handwritten handlers

1. The app binary command `audit` has three actions:
   - `audit list --resource T [--id N] [--actor EMAIL] [--action A] [--since D] [--until D] [--unattributed] [--limit 100] [--json]`.
     `--limit` defaults to 100 and has the maximum 1000. A larger value fails with a `Try:` line that names `audit export`.
   - `audit export --since D --until D --format jsonl|csv [--out FILE]`. It reads the table in keyset pages of 1000 rows (`id > last`) and writes each page at once. It never holds the full result in memory.
     It prints the SHA-256 of the file.
   - `audit prune --before D --reason TEXT --yes`
2. Example: `api audit list --resource work_orders --id 42`. The output has one line for each event, like the History page:
   `2026-11-03 14:40  ben@example.com (staff)  update  status: assigned -> done`
3. A missing flag fails and names the flag. The command never prompts. Every error ends with a `Try:` line.
4. The command reads the database through `DATABASE_URL`. It needs no web server.
5. The command does not record that it read the trail. Shell access is outside this model.
6. The recipe for a handwritten handler:
   - Wrap each write in `audit.InTx` and read with `s.Queries.WithTx(tx)`.
   - For a raw SQL job, put `audit.WithActor` in the context and write through `audit.InTx`.
   - For an event that is not a row change, call `audit.Record`.
   - For a history route, copy the generated handler: guard, owner filter, limits, then `audit.List`.
   Until a handler uses the recipe, its events are `unattributed`.

### 10. Retention and size

1. The default retention is forever. The trail never prunes by itself.
2. `audit prune` is the one delete path. It refuses an age below 1 day. It writes an `audit.prune` event with the range, the count, and the reason.
3. The docs advise pruning only sign-in events. Row history has no advised age.
4. The size is about 1 KB for each event with the indexes. 10,000 events a day is about 3.6 GB a year.
5. Partitioning is out of scope.

### 11. Personal data

1. An event keeps the email of the actor as it was at the time.
2. A request to erase a person conflicts with a durable trail. This version has no pseudonymize command.
   The docs state this. The owner decides the legal basis.

### 12. Failure modes

| Case | Result |
|---|---|
| The event write fails for a row change | The data statement fails and the transaction rolls back. |
| The event write fails for sign-in or sign-out | The code logs the error. The user continues. |
| A write without an actor | The event is `unattributed`. `audit list --unattributed` shows it. |
| The actor user is deleted later | The snapshot columns stay. |
| A new binary meets an old schema | Row writes on audited tables are not affected, because the triggers are absent. Sign-in events fail open. The history endpoint answers 500. |
| A table has a composite key | `record_id` joins the key columns (decision 3.5). The first insert does not fail. |
| A mass update of N rows | N events in one transaction. The docs advise a batch size and a reason. |
| `TRUNCATE` of an audited table | One `truncate` event with `record_id` `*`. |
| `DROP TABLE` and other DDL | Not captured. The migration files in git are the record. |
| A request for a page over the limit | 422 with a `Try:` line. |
| Tamper by the application or the database owner | Not prevented (decision 8). |

### 13. Upgrade of existing apps

1. The module is opt-in. First run `spinup upgrade`. Then run `spinup module add audit`. It makes a branch for review.
2. The upgrade adds the auth migrations that the app lacks (always `00005_users_role.sql`) before the audit migration `00006`. The new files are `00006_audit_events.sql` and `history-list.svelte`. Both are new, so the planner creates them.
3. `main.go` is a `scaffold` file. The owner adds the lines of decision 4.8 by hand. The `Next:` text of `module add` prints them.
4. The doctor check `audit-wired` (scope Local) warns when the module is on and `main.go` lacks `audit.Command(` or `authsink.`. It also warns for an unaudited `user_identities` table (decision 3.9).
   The package registers itself with `Register(...)` and a blank import in `cmd/spinup/wire_gen.go`.
5. Existing tables need `spinup g audit <resource>`. Existing handlers need the recipe of decision 9.6.
6. Kit release:
   - The new API ships in the next kit tag. This run does not set the version number and does not change the kit pin in `stackversions.yaml` or in the packs. The tag and the pin bump are the decision of the owner.
   - `kit/MIGRATION.json` keeps `"version": "0.1.0"` and has one more `manual` step, `audit-schema`: "Run spinup db migrate before you start the new binary when the audit module is on. Try: run spinup db migrate up."
   - `kit/api/current.txt` holds the new API. The pre-seed regenerated it with `SPINUP_GOLDEN_UPDATE=1 go test ./internal/apicompat` for the contract stubs. The main loop regenerates it again after unit K. `kit/api/minor.txt` stays `0.1`: the release process changes it (`docs/policy/kit-compat.md`).
   - Generated apps in tests and in the trial use `SPINUP_KIT_REPLACE`, because the kit tag does not exist yet.
7. Digest: the new pack `audit` gets a new entry in `packs.digest`. The pre-seed set it to `none`, because the pack adds only new files.
   The change to `main.go.tmpl` in the `api` pack (unit P) changes the `api` entry. The main loop sets it to `none` at integration, because the change adds merge-friendly lines. Lanes do not commit `packs.digest`.
   Unit P changes the `audit` entry again when it adds files. The main loop sets it to `none` again.
8. The todo app has auth and no jobs, no mail, and no OIDC. It has the tables `users` and `todos`, and no `user_identities`. It is the dogfood target of the run plan.

### 14. Out of scope

- Read access to events of other tables through the API, and an audit page for managers.
- A hash chain, external anchoring, a WORM store, and a separate database role (the role split is a documented hardening step only).
- Pseudonymize and erase commands.
- Table partitioning and automatic pruning.
- Audit of reads and of DDL.
- A diff view with field labels from the resource schema.

### 15. Tests

1. Kit database tests (`DATABASE_URL`):
   - insert, update, delete, and truncate each write the right event with the right values
   - a table with a composite key writes `record_id` as `a:b`, and an OIDC identity insert into `user_identities` succeeds and writes one event
   - a no-op update writes nothing, a redacted column shows no value, and a large value is truncated with a digest
   - `InTx` binds the actor, and a write outside `InTx` is `unattributed`
   - `Bind` on a pool returns `ErrNotInTx`, and `Record` on a pool writes the explicit actor
   - the actor setting does not leak to the next use of a pooled connection
   - a job context with `WithActor` through `InTx` writes the system actor
   - `UPDATE`, `DELETE`, and `TRUNCATE` on `audit_events` fail, and `Prune` works
   - a failed trigger write rolls back the data change
   - `register`, `sign_in`, `sign_out`, `lockout`, `SetRole`, and `password_reset` write the right events
   - a failing sink in `SetRole` rolls back the role change, and a failing sink in `sign_in` does not stop the sign-in
   - a migration that starts with the `set_config` line of decision 4.10 writes `system` events with the source `migrate`, and one without the line writes `unattributed`
   - the same tests run on Postgres 16, 17, and 18 at the gate
2. Migration tests run the audit migration up and down through goose (not only through `psql`). They run on a schema with only auth 00002 (the todo shape), and on a schema with 00002 to 00005.
   The pre-seed wrote them in `kit/audit/schema_test.go`. They skip without `DATABASE_URL`. Unit K keeps the file.
3. Generator tests: spec errors with `Try:` lines, the goldens `gen-resource-audit` and `gen-audit`, the generated CRUD and history tests (including the 422 on large `limit`), and `--no-audit`.
   The goldens for apps without the module do not change.
4. A pack test proves that sqlc parses the audit migration.
5. Script tests: create an app with the module, generate a resource, update it, and run `audit list`. Run the retrofit on an app without the trail. Run the no-TTY error.
6. Upgrade corpus cases:
   - a conflicting `main.go` expects the `audit-wired` warning
   - an app-owned `00006_*.sql` expects the clash warning
   - an app with resource migrations at 00003 to 00006 plus the pack migrations expects the clash warning for each duplicate number
   - the todo shape (migrations 00001 to 00003, no mail, no OIDC) expects 00005 then 00006 and no warning

### 16. Docs

1. `docs/audit.md` has runnable blocks: the module, `g resource`, `g audit`, the History page, the `audit` command, the handler recipe, retention, the role and filter table, and the limits of decision 8.
2. `docs/commands/generate-resource.md` gets the new flags. `docs/commands/generate-audit.md` is new. `docs/auth.md` lists the new events. `docs/index.md` links `docs/audit.md`.

## Consequences

- Every change to an audited table has a trail, even when a developer forgets the helper. A forgotten helper shows as `unattributed`, not as a gap.
- Each audited write costs one more insert and one `set_config` call.
- A failure of the trigger stops the business write. This is the safer choice for an auditor.
- The trail resists accident and casual edits. It does not resist application code that is hostile or has an injection flaw, and it does not resist the database owner. The docs say so.
- Existing apps need a module step, a migration, the next kit tag, and a recipe for old handlers.

## Appendix: Pre-seed facts

The pre-seed (unit P0 of run 20261031-201904) measured these facts on 2026-10-31.

### Migration on Postgres 16, 17, and 18 (step 0.3)

1. The migration `internal/templates/packs/audit/files/api/db/migrations/00006_audit_events.sql` ran up and down through goose, with the kit migrator `db.NewMigrator`.
   The images were the pinned `postgres:16.15`, `postgres:17.11`, and `postgres:18.6-trixie` of `stackversions.yaml`, with their digests.
2. The test is `kit/audit/schema_test.go`. It skips without `DATABASE_URL`. It has three tests:
   - `TestSchemaTodoShape`: init, auth `00002`, a `todos` resource, the audit migration, a retrofit `00007_audit_todos.sql`, and two data migrations.
   - `TestSchemaOIDCShape`: init and auth `00002` to `00005` (with password reset and OIDC), then the audit migration.
   - `TestSchemaOIDCAfterAudit`: the audit migration first, then auth `00004` out of order.
3. Results, the same on all three versions:
   - Insert, update, delete, and truncate each write one event with the values of decision 3.3. A no-op update and an update of `updated_at` only write nothing.
   - The OIDC identity insert succeeds and writes `record_id` `google:1234567`. An OIDC user with a NULL `password_hash` writes no `password_hash` key on insert.
   - `password_hash` and a column that the trigger arguments name are `{"redacted": true}`. No event holds the hash.
   - A 5000-byte value becomes `{"truncated": true, "bytes": 5000, "sha256": ...}`.
   - The actor of a transaction with the setting is in the event. The next write on the pool, outside that transaction, is `unattributed` with the source `sql`.
   - `UPDATE`, `DELETE`, and `TRUNCATE` on `audit_events` fail with `audit: <OP> on audit_events is not allowed`. An action that breaks the check constraint fails.
   - `audit_prune` refuses a time less than 1 day old and an empty reason. It deletes the older events, writes one `audit.prune` event with `{"before": "<UTC time>", "count": N}`, the reason, and the actor of the setting, and turns the prune setting off again. A `DELETE` after it in the same transaction still fails.
   - A trigger with a key column that does not exist fails the insert, and the row is not written.
   - `audit_append` turns empty strings into NULL and an empty kind into `unattributed`.
   - In the todo shape, the migration attaches `users_audit` and `users_audit_truncate` and skips `user_identities` without an error.
   - In the OIDC shape, it attaches `user_identities_audit` and `user_identities_audit_truncate`. `TRUNCATE users, user_identities, password_resets` writes one `truncate` event for each audited table.
   - When auth `00004` runs after the audit migration, `user_identities` has no trigger and its writes have no events. The doctor check `audit-wired` and `spinup g audit user_identities` cover this case.
   - Down removes every `audit_*` function, the table, and every `*_audit*` trigger. Up again works.
4. Decision 4.10 holds: `SELECT set_config('spinup.audit', '{"kind":"system","source":"migrate","reason":"backfill"}', true);` on the first line of a goose migration gives `system` events with the source `migrate` and the reason `backfill`.

### sqlc (step 0.3b)

1. The scratch app came from the built `spinup new app2 --modules auth,audit --module example.com/app2 --no-git --non-interactive` with `SPINUP_KIT_REPLACE`.
   The new app has `00001_init.sql`, `00002_create_auth.sql`, `00005_users_role.sql`, and `00006_audit_events.sql`. Its `spinup.toml` has `modules = [ "auth", "audit" ]`.
2. `spinup db sqlc` (sqlc v1.31.1 from the tools module) parsed the migration offline. A query file that reads `audit_events` and calls `audit_append` generated code.
   The API module built.
3. `internal/store/db.go` has `func (q *Queries) WithTx(tx pgx.Tx) *Queries`.

### oapi-codegen (step 0.4)

See ADR 0026, appendix "Pre-seed facts".

### Modules and the Get guard (step 0.5)

1. A pack becomes a module with no Go registration (decision 2.1). The pre-seed added none.
2. The `--api-only` check of `spinup new` read only the direct `requires` of a pack. With the audit pack, `testdata/script/new_api.txtar` (`available modules: backups, ci, deploy, jobs`) would fail. The pre-seed changed `selectPacks` in `internal/gen/newapp/newapp.go` to follow the requirement chain.
3. The role and owner-filter table is in decision 6.2.

### Kit API (step 9)

1. `kit/api/current.txt` holds the stubs of `kit/audit`, `kit/auth/events.go`, `kit/httpx/attachment.go`, and `kit/pdf`. `kit/api/minor.txt` stays `0.1`.
2. `kit/MIGRATION.json` has the step `audit-schema` (ruling R1).
