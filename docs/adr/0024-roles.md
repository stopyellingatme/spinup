# ADR 0024: Roles guard

Status: proposed
Date: 2026-10-31
Extends: ADR 0015 section 1 (resource ownership)

## Context

The first real app needs two roles on one site.
Other apps need other roles.
Today the auth module knows only the user and the owner of a row.
No role exists in the database, the kit, the session JSON, the generator, or the web pack.
This ADR adds a role list that each app declares, and a guard at each layer.

## Decisions

### 1. One source of truth: the `[auth]` table

1. Each app declares its roles in the `[auth]` table of `spinup.toml`.
2. The keys are `roles` (a list of names), `admin_role` (one name), and `default_role` (one name).
3. Example: `roles = ["manager", "staff"]`, `admin_role = "manager"`, `default_role = "staff"`.
4. If the table is absent, or a key is absent, the value is the default from decision 3.
   An app that never declares roles behaves like an app with the example values.
5. The default is a value in `internal/project` and in the kit. It is not a fixed role set.
   The kit uses it only when the app does not call `auth.WithRoles`.
6. A role name matches `^[a-z][a-z0-9_]{0,31}$`. The list has 1 to 16 unique names.
   `admin_role` and `default_role` must be in the list.
   A name has no double underscore and no trailing underscore.
   The `RoleConst` values of the list must be unique.
   No `RoleConst` value is `Set`, because the generated file declares `var Set`.
7. `project.Config.Validate` checks all rules in decision 6. The error names the rule and the value, and ends with a `Try:` line.
8. `admin_role` has two uses. The first-user rule gives it to the first account (decision 6.3).
   The last-admin rule keeps at least one account that holds it (decision 4.11).
9. `default_role` is the role of self-service accounts and OIDC accounts.
   It is also the role that an empty stored role reads as, with one exception (decision 4.8).
10. A role name is an app word. The kit, the generator, and the web pack never test for a name.

### 2. Config in `internal/project`

1. `internal/project/project.go` is not on the shared-files list in `CLAUDE.md`.
   Four units read `project.Config`, so the main loop changes it in the pre-seed (Section B).
   Add `project.go` and `roles.go` to the shared list in `CLAUDE.md`.
2. `Config` gets the field `Auth struct { Roles []string; AdminRole string; DefaultRole string }` with the tag `toml:"auth"`.
3. `Parse` adds `"auth"` to the list of known keys. The key then stays out of `Extra`.
4. `func (c *Config) AuthRoles() AuthRoles` returns the effective set with the defaults applied.
5. `internal/project/roles.go` holds these pure functions:
   - `AuthRoles` (fields `Names`, `Admin`, `Default`) and its `Validate`.
   - `RoleConst(name string) string`. It turns `front_desk` into `FrontDesk`.
   - `RenderRolesFile(r AuthRoles) []byte`. It returns the exact bytes of the generated file (decision 3.2).
   - `RolesFilePath = "api/internal/roles/roles.gen.go"`.
6. The resource generator did not read `spinup.toml` before this ADR. It reads `api/go.mod` and `spinup.lock`.
   After this ADR it calls `project.Load(in.Root)`.
   If `spinup.toml` is absent (unit tests), it uses the defaults.

### 3. The generated Go file

1. The file is `api/internal/roles/roles.gen.go`, package `roles`. It belongs to the `auth` pack.
2. The file holds a "Code generated ... DO NOT EDIT." header, one constant for each role, and one value:
   ```go
   const (
       Manager = "manager"
       Staff   = "staff"
   )

   // Set is the role set of this app. Pass it to auth.WithRoles.
   var Set = auth.Roles{Names: []string{Manager, Staff}, Admin: Manager, Default: Staff}
   ```
3. The file class is `managed`. A new app gets the default bytes from the template.
   An upgrade of an old app creates the file with the default bytes (`ActionCreate`).
4. `spinup g roles` writes the file through `Output.Edits`. The file stays a pack template in the lock, so an upgrade 3-way merges it.
   The base of the merge is the template bytes of the locked version, not always the default bytes.
   A custom `[auth]` in an old app does not change the upgrade result.
   The upgrade creates the default file, and `main.go` may not pass `WithRoles`.
   After such an upgrade, the owner must run `spinup g roles` (decision 11.10).
5. The command `spinup g roles` regenerates the file from `spinup.toml`.
   The generator returns a `vfs.Plan` with one file. It lists the path in `Output.Edits`.
   The runner then updates the file without `--force` and keeps the lock template.
6. The generator has no post-step. No other generator edits `spinup.toml`, so nothing needs to start it.
   The user runs `spinup g roles` after a change to `[auth]`. The next-step line of every command that shows `[auth]` says so.
7. The doctor check `roles-drift` (package `internal/doctor/rolesdrift`, scope Local) checks three things:
   - The file differs from `RenderRolesFile(config.AuthRoles())`: `warn`. Message: `api/internal/roles/roles.gen.go does not match [auth] in spinup.toml.` `Try: spinup g roles`.
   - The file is missing in an app with the `auth` module: `warn`, same `Try:` line.
   - `api/cmd/api/main.go` has no `auth.WithRoles(` call: `warn`. Message: `api/cmd/api/main.go does not pass the role set to the auth module.` `Try: add auth.WithRoles(roles.Set), auth.WithRegistration(...) and app.WithCommand("user", auth.UserCommand(a)) to main.go.`
   - No `auth` module, or outside a project: `skip`.
   The check runs in quick mode, because it reads three small files.
8. A role that you remove from `spinup.toml` and then regenerate also removes its constant.
   Generated handlers that use the constant then fail to compile. This is wanted: the error finds each use.

### 4. Kit API (package `kit/auth`)

The change adds API. The only change to a public type is the new `User` field.

1. `type Roles struct { Names []string; Admin, Default string }`.
   - `func (r Roles) Validate() error` applies the rules of decision 1.6.
   - `func (r Roles) Valid(role string) bool` reports if `role` is a declared name.
   - `func DefaultRoles() Roles` returns `manager`, `staff`, admin `manager`, default `staff`.
2. `func WithRoles(r Roles) Option`. If the app does not pass it, the set is `DefaultRoles()`.
3. `auth.New` calls `Validate` and returns the error. The app then fails at startup with a `Try:` line.
4. `func (a *Auth) Roles() Roles` returns a copy of the set.
5. `User` gets `Role string` with the tag `json:"role"`.
6. `func (u User) HasRole(roles ...string) bool` is true when `u.Role` equals a listed name. It is false for an empty list.
   A stored role that the app no longer declares never equals a name of the set, so it is false for every declared role.
7. `func (a *Auth) RequireRole(roles ...string) func(http.Handler) http.Handler`. It lives in `required.go`.
   - If no user exists, it answers 401 through `unauthorized`.
   - If the role is not listed, it answers 403 through `forbidden(w, r)`.
   - `forbidden` calls `noStore(w)` and `httpx.WriteProblem` with status 403. It sets no `WWW-Authenticate` header.
   - The 403 detail is `Your role cannot use this endpoint. Try: sign in with an account that has a required role.`
   - `httpx.Problem` has no hint field. The `Try:` text is the end of `Detail`. This ADR makes no change to `httpx.Problem`.
   - It is a method, so it can check the names against the set. If you call it with no name, or with an undeclared name, it panics at construction. The error then appears at startup.
8. Store queries use these column forms:
   - `userColumns = "id, email, role"` for queries on `users` alone (`userByID`, `credentialsByEmail`).
   - `userColumnsU = "u.id, u.email, u.role"` for the `oidcUser` join of `users u` with `user_identities i`.
     `user_identities` also has an `email` column, so the unqualified form is ambiguous in the join.
   - The `INSERT ... RETURNING` and `SELECT ... FOR UPDATE` queries (in `createUser` and `oidcUser`) are written out in full. They do not use a shared constant.
   - `createUser` and `oidcUser` always set `role` in their `INSERT`, because the column default is the empty string.
   After each read, one function maps the stored role to the effective role:
   - A non-empty role stays as it is, also when the app no longer declares it.
   - An empty role reads as `default_role`.
   - Exception: if `default_role` equals `admin_role`, an empty role reads as the empty string. It then matches no name, and `HasRole` is false. An empty role is never an administrator.
9. `type RegistrationMode string` with `RegistrationOpen`, `RegistrationFirstUser`, and `RegistrationClosed`.
   `func WithRegistration(m RegistrationMode) Option`. The default is `RegistrationOpen`.
   `func ParseRegistration(s string) (RegistrationMode, error)` parses `AUTH_REGISTRATION`.
   An invalid value returns `AUTH_REGISTRATION: invalid value "x". Valid values: open, first-user, closed. Try: set AUTH_REGISTRATION to one of these values.` The app then fails at startup.
10. `func (a *Auth) CreateUser(ctx context.Context, email, password, role string) (User, error)`.
    It is for trusted code only. It returns `ErrUnknownRole` if the role is not declared.
    In `first-user` mode it takes the lock of decision 6.4.
    The register handler never reads a role from the request body.
11. `func (a *Auth) SetRole(ctx context.Context, email, role string) error`.
    - The email goes through `normalizeEmail`, as in login.
    - Each sentinel error has a message that ends with a `Try:` line:
      - `ErrUnknownRole`: `role "x" is not declared. Declared roles: manager, staff. Try: use one of the declared roles, or add the role to [auth] in spinup.toml and run spinup g roles.`
      - `ErrUserNotFound`: `no account has this email. Try: run "user list" to see the accounts.`
      - `ErrLastAdmin`: `the change would remove the last account with role "manager". Try: give the role "manager" to another account first.` The role name in the message is `admin_role`.
    - `SetRole` runs in one transaction. It takes the lock of decision 6.4 first. It then reads the target row with `SELECT ... FOR UPDATE`.
    - The last-admin check runs only when the stored role of the target equals `admin_role` and the new role differs.
      A stored empty role is never `admin_role`, so the check does not run for it.
      The check counts rows whose stored role equals `admin_role`. If the count is 1, the target is the last administrator and the call returns `ErrLastAdmin`.
    - If the set has no administrator (for example after `admin_role` was renamed in `[auth]`), the check never runs. The operator can then repair the roles with any `SetRole` call.
    - Promotion and a change to a role other than `admin_role` for a non-administrator never run the check.
12. `func (a *Auth) ListUsers(ctx context.Context) ([]User, error)` and `func (a *Auth) BackfillRoles(ctx context.Context) (int64, error)`.
    `BackfillRoles` writes `default_role` into every row with an empty role and returns the row count.
    It refuses when `default_role` equals `admin_role`, with the error `default_role equals admin_role, so empty roles cannot be filled safely. Try: run "user set-role" for each account.`
13. `func UserCommand(a *Auth) func(ctx context.Context, args []string) error`. It fits `app.WithCommand` (`kit/app/options.go:46`).
14. `authtest.AsRole(h http.Handler, userID int64, role string) http.Handler` and `authtest.AsUser(h http.Handler, u auth.User) http.Handler`.
    `authtest.As` stays unchanged and gives an empty role.
15. The rate limiter and CSRF do not change. The role check runs after both.
16. The kit has no `RoleManager`, `RoleStaff`, or package-level `ValidRole`. The generated `roles` package holds the constants.
17. The kit migrator (`kit/db/migrate.go`) passes `goose.WithAllowOutofOrder(true)` (decision 5.11).

### 5. Data model and migration

1. The role is a text column on `users`. It has no CHECK constraint. The kit checks the role in code.
2. Add the pack file `api/db/migrations/00005_users_role.sql`. Its class is `seed`.
3. The Up step is `ALTER TABLE users ADD COLUMN role text NOT NULL DEFAULT ''`.
4. The Down step is `ALTER TABLE users DROP COLUMN role`.
5. The migration names no role. An empty role reads as described in decision 4.8.
6. Existing users keep an empty role. The empty role never gives administrator rights (decision 4.8).
7. After the migration, the owner runs `user backfill` once. It writes `default_role` into the empty rows, so each row is explicit.
   Then a later change of `default_role` does not move old rows. Until then, an empty row follows `default_role`. The docs state this.
8. New rows always store a named role. Only rows from before the migration are empty.
9. Do not edit `00002_create_auth.sql`. A seed file never changes after the first render.
10. The role stays on `users`. A later `memberships` table can replace the source of `User.Role`. The guard API does not change in that move.
11. Migration order. The migrator did not allow out-of-order apply. Goose then rejects a pending migration with a version below the latest applied version.
    Two cases break: an app that already applied `00004` or higher (from `g resource`), and an app that enables `mail` (`00003`) or `auth_oidc` (`00004`) after `00005` is applied.
    The kit migrator now passes `goose.WithAllowOutofOrder(true)`. Pending lower versions apply, in version order, and the migrate no longer fails.
    A script test proves both cases (decision 12.7).
    A name clash (an app-owned `00005_*.sql`) is a separate case (decision 11.2).

### 6. Session JSON and registration

1. The cookie still stores only the user ID. `loadUser` reads the role on each request. A role change takes effect on the next request.
2. `GET /api/session`, `register`, and `login` return `{"user":{"id","email","role"}}`. If an OpenAPI schema for the session user exists, add `role` to it.
3. Registration has three modes. The app binary reads `AUTH_REGISTRATION` and passes it to `WithRegistration`.
   - `open`: anyone can register and gets `default_role`. This is the default.
   - `first-user`: registration is open only while `users` is empty. The first user gets `admin_role`. After that, registration answers 403.
   - `closed`: registration always answers 403.
4. One lock protects the first-user rule: `pg_advisory_xact_lock(hashtext('spinup.auth.users'))`.
   In `first-user` mode, `register`, `oidcUser` (account creation), and `CreateUser` take this lock in their transaction, then count the rows, then insert.
   `SetRole` takes the same lock (decision 4.11). Two parallel requests cannot both become the first administrator.
5. OIDC auto-create follows the mode:
   - `open`: the new account gets `default_role`.
   - `first-user`: OIDC creates an account only while `users` is empty, and that account gets `admin_role`. After that, OIDC does not create accounts. A known identity still signs in.
   - `closed`: OIDC does not create accounts.
   A refused OIDC creation uses the same text as the registration 403.
6. The 403 problem for a refused registration has the detail `Registration is closed. Try: ask an administrator to create your account.`
7. For a new app with a public URL, use `first-user` or `closed`. Register the admin before you publish the site.
   A stranger can take the first slot of `first-user` mode if the site is public and empty.

### 7. Role assignment: operator CLI only

1. The first version has no manager page and no route to change a role. Only the operator CLI changes a role.
2. The app binary command `user` has four actions. It registers with `app.WithCommand("user", auth.UserCommand(a))`.
   - `user create --email E --role R --password-stdin`
   - `user set-role --email E --role R`
   - `user list`
   - `user backfill`
3. The command checks `--role` against `a.Roles()`. The set comes from the generated file, so it matches `spinup.toml`.
4. If a flag is missing, the command fails and names the flag. It never prompts.
   Example: `user create: --role is required. Declared roles: manager, staff. Try: user create --email a@b.c --role staff --password-stdin.`
5. An unknown role fails with the declared list and the line `Try: use one of the declared roles, or add the role to [auth] in spinup.toml and run spinup g roles.`
6. `user list` prints the email and the role. It marks a stored role that the app no longer declares with `(undeclared)`.
   It marks an empty role as `<default_role> (default)`, or as `(none)` when `default_role` equals `admin_role`.
7. A CLI error never reveals if an email exists, except in `user set-role`, which is for an operator.
8. The owner of an upgraded app runs `user set-role --email E --role <admin_role>` once, and then `user backfill`.
9. The `user` command exists only if `main.go` registers it. The `api` pack declares `main.go` as `scaffold`, so an upgrade can leave it without the line.
   Then the owner uses this SQL fallback: `UPDATE users SET role = '<admin_role>' WHERE email = lower('<email>');`.
   The docs and the upgrade notes show this fallback. `roles-drift` warns about a missing `WithCommand` (decision 3.7).
10. An undeclared stored role keeps its old text. If the name returns to `[auth]` later, the old rows get power again, maybe with a new meaning.
    This is an accepted risk. `user list` marks such rows, and the docs tell the owner to run `user set-role` for them before a rename or a removal.

### 8. Generator

1. Add the flag `--role` to `g resource`. It takes a comma list, for example `--role manager` or `--role manager,staff`. It applies to all five operations.
2. If you give no `--role`, the resource checks only that a user exists, as before.
3. `Spec` gets the field `Roles []string`. The pre-seed adds it.
4. `build()` returns an error with a `Try:` line in these cases:
   - The auth module is missing. `Try: spinup add auth`.
   - A role is not in `config.AuthRoles().Names`. `Try: use one of: <declared list>, or add the role to [auth] and run spinup g roles`.
   - The list is empty.
   - `api/internal/roles/roles.gen.go` is missing or differs from `RenderRolesFile`. `Try: spinup g roles`.
5. `Owned` and `Shared` do not change. A role does not change the `user_id` filter.
6. If you use `--role` without `--shared`, the plan prints a note. A user then sees only the rows that this user owns. `Try: add --shared to share rows between users.`
7. `openapi.go` adds a `403` response to every operation when `Roles` is set.
8. In `handlers.go`, the `guard` closure always binds `user` when `Roles` is set.
   After the `ok` check, it emits `if !user.HasRole(roles.Manager, ...)` and returns `gen.<Op>403ApplicationProblemPlusJSONResponse`.
   The constants come from `RoleConst`. The file imports `<module>/internal/roles`.
9. The check is in the handler, not in `routes.gen.go`. The generator sees one spec at a time. A route table needs persisted state.
10. Known cost: a wrong-role caller who sends bad input gets 400 or 422 before 403. The caller learns only the schema shape. The docs state this.
11. `g page --server --role R` emits `requireRole(locals, url, ...)` in the load and in each action. The role names are string literals in TypeScript.
    Without `--server`, it fails. `Try: add --server, because only a server load can guard a page.`
12. Resource pages for a resource with roles call `requireRole` in the load and in each action.
13. Generated tests:
    - The CRUD test and the bad-input tests use `authtest.AsRole` with an allowed role.
    - A new table test sends a request from a role outside the set. It expects 403 on every operation. If every declared role is allowed, the test uses an undeclared role name.
    - Tests for `authenticationTest` stay and still expect 401.
14. Add the generator `roles` (`spinup g roles`), described in decision 3.5. It has no flags. With no change, it reports `nothing to do`.

### 9. Web guard and navigation

1. `app.d.ts` adds `role: string` to `App.User`. `hooks.server.ts` does not change.
2. `lib/server/auth.ts` (managed) adds `requireRole(locals, url, ...roles)`.
   - It calls `requireUser`, so a missing user redirects 303 to `/login?redirectTo=...`.
   - If the role is not listed, it calls `error(403, 'Your role cannot open this page. Try: sign in with an account that has a required role.')`. The existing `+error.svelte` shows it.
3. Navigation does not filter by role in the first version. Hiding an item would be cosmetic. `PlanNav` rebuilds `nav.gen.ts` from disk and has no source for roles.
4. The Go guard and `requireRole` are the enforcement. `home` stays one value for all roles.

### 10. Errors

1. Every error says what failed and why. Every error ends with a `Try:` line.
   This includes the sentinel errors (decision 4.11), the 403 answers (decisions 4.7 and 6.6), and the `AUTH_REGISTRATION` error (decision 4.9).
2. The kit answers 401 for no user and 403 for a wrong role. Both use `application/problem+json` and `Cache-Control: no-store`.
3. Startup errors from `Roles.Validate` name the rule and the value that failed.

### 11. Upgrade of existing apps

1. `00005_users_role.sql` and `api/internal/roles/roles.gen.go` are new untracked files. The planner gives each `ActionCreate`.
2. If the app already has a different `00005_*.sql` file, two files share version 5. The upgrade plan then prints a warning.
   `Try: rename your migration to the next free number, then run spinup upgrade again.`
3. Out-of-order apply is handled by the kit migrator (decision 5.11). A version gap is valid in goose.
   The pre-seed changes the migrator and proves it with a kit test. A script test covers an app with an applied `00004` (decision 12.7).
4. `main.go.tmpl` has class `scaffold`, not `managed`. A scaffold file is 3-way merged, and an overlap with an app edit gives `ActionConflict`.
   The todo app edited `main.go`. The `WithRoles`, `WithRegistration`, and `WithCommand` lines can conflict or be skipped.
   Then the app still runs, and the kit uses `DefaultRoles()`. A custom `[auth]` has no effect until the owner adds the lines.
   The `user` command is then missing, so the owner uses the SQL fallback of decision 7.9.
   `roles-drift` warns about the missing lines. The upgrade notes list the lines to add by hand.
5. `auth.ts`, `auth.test.ts`, and `app.d.ts` are managed files and merge three-way.
6. `spinup.toml` is app-owned. The upgrade does not edit it.
7. No codemod is needed. The `packs.digest` entries for the `auth`, `web`, and `api` packs get `none`. The reason is new files plus changes that the 3-way merge handles.
8. `kit/api/current.txt` gets additions and the removal of nothing that a release shipped. The one risk is an unkeyed literal `auth.User{1, "a"}`, which breaks.
   Before v1 this is accepted. `SPINUP_API_BREAK` is not set. `behavior_version` stays at 1.
9. The kit `MIGRATION.json` gets a `manual` step: run `spinup db migrate` before you start the new binary.
10. If `spinup.toml` has an `[auth]` table and the app has no `roles.gen.go`, the upgrade plan prints a note.
    `Try: run spinup g roles after the upgrade.` The upgrade creates the default file, and `roles-drift` warns until the owner regenerates it.
11. Deploy already migrates before it starts the new binary. If you run the new kit against an old schema, login fails with a 500.

### 12. Tests

1. `internal/project`: parse and default cases for `[auth]`, each `Validate` rule, `RoleConst`, and a golden for `RenderRolesFile`.
   Validate cases: `a_b` with `a__b`, `a_` with `a`, a name with a trailing underscore, and the name `set`. Each one is rejected.
   A test proves that the default render equals the pack template bytes.
2. Kit table tests: `Roles.Validate`, `HasRole`, `Auth.RequireRole` for 401, 403, and 200, the construction panic, and the 403 body (with `Try:` in `Detail`) and headers.
3. Kit database tests (with `DATABASE_URL`):
   - 20 parallel registrations in `first-user` mode make exactly one admin.
   - One OIDC callback and one register request in parallel, in `first-user` mode, make exactly one account.
   - The three registration modes, for register and for OIDC.
   - `SetRole` returns `ErrUnknownRole`, `ErrUserNotFound`, and `ErrLastAdmin`, each with a `Try:` line.
   - `SetRole` in a zero-administrator state (renamed `admin_role`) succeeds for a non-administrator target.
   - `SetRole` for the only administrator to a lower role fails. A parallel pair of demotions of two administrators leaves one.
   - An empty role reads as `default_role`. With `default_role` equal to `admin_role`, an empty role gives `HasRole` false and does not count as an administrator.
   - `BackfillRoles` fills the empty rows and refuses when `default_role` equals `admin_role`.
   - A row with an undeclared role gives `HasRole` false for every name.
   - A role change applies to the next request with the same cookie.
   - An OIDC sign-in with an existing identity works (the join query), and the OIDC create stores `default_role`.
   - `GET /api/session` returns `role`.
   - A custom set (for example `owner`, `cook`) works in all of the above, including the OIDC join.
   - `ParseRegistration` rejects an invalid value.
4. Kit migrator test (`kit/db`): apply `00001` to `00005`, add a pending `00004`, migrate, and expect success.
5. `apicompat`: update `current.txt`.
6. Generator: spec tests for the `--role` errors (including the drift error), golden trees for the `auth`, `web`, `api`, `gen-resource`, `gen-resource-auth`, and `gen-resource-shared` goldens,
   the 403 table test, a `g roles` test with a custom set, and a `roles-drift` doctor test (including the missing `WithRoles` warning).
7. Web: vitest cases for `requireRole` (redirect, 403, pass).
8. Script tests:
   - Create an app, run `g resource --role manager`, register, expect 403, run `user set-role`, expect 200.
   - Edit `[auth]` to custom roles, run `g roles`, then `g resource --role <custom>`. Expect `doctor` to report no drift. Edit `[auth]` again without `g roles` and expect the drift warning.
   - The no-TTY error.
   - Migration order: an app with an applied `00004` gets `00005` and migrates. An app with `00005` applied then adds a `00004` migration and migrates.
9. Upgrade corpus: an old app with user rows gets `00005` and `roles.gen.go`, keeps its edited files, and every user reads as `default_role`.
   A second case has an app-owned `00005` and expects the warning.
   A third case has an edited `main.go` that conflicts, and expects the conflict report and the `roles-drift` warning.
   A fourth case has a custom `[auth]` before the upgrade, and expects the `spinup g roles` note.

### 13. Docs

1. `docs/auth.md` gets a Roles section with runnable blocks: the `[auth]` table, `spinup g roles`, `RequireRole`, `HasRole`, `user`, `requireRole`, and the registration modes.
2. Update every `{"user":{"id","email"}}` example in `docs/auth.md`.
3. `docs/commands/resource.md` and the page command page get a `--role` example. Add `docs/commands/roles.md`.
4. The docs state the 400-before-403 order, the owner filter note, the effect of an empty role and of `user backfill` (decision 5.7), and the accepted risk of undeclared stored roles (decision 7.10).
5. The upgrade notes list the first-admin steps, the SQL fallback, the migrate-before-run order, and the `main.go` lines to add by hand.

### 14. Out of scope

- A role table in the database, and role changes at run time without a restart.
- Separate read and write roles, and permissions below the role level.
- A role that sees all rows on an owned resource.
- A manager page, manager routes, or any route that changes a role.
- Navigation filter and per-role home page.
- Memberships and organizations.
- Invite emails.
- OIDC claim mapping to roles.
- A role in the cookie.
- A route-level 403 before parameter binding.
- A `spinup new` flag for the role list. The user edits `spinup.toml` and runs `spinup g roles`.
- Any change to the rate limit or CSRF.
- A `spinup`-side `user set-role` that does not need `main.go`. The SQL fallback covers this case before v1.
- A new field in `httpx.Problem`.

## Consequences

- A guard works at three layers: Go handler, resource generator, and web page.
- Each app owns its role words. The kit, the generator, and the web pack have no role name in their logic.
- An app change to `[auth]` needs one more command, `spinup g roles`. The doctor check finds a missed run.
- An existing app must run one migration, set its first admin, and run `user backfill`.
- An existing app with an edited `main.go` must add three lines by hand. The doctor check finds a missed edit.
- Open registration with a role guard gives every stranger the `default_role`. Use `first-user` or `closed` for a public site.
- The kit migrator accepts out-of-order migrations for every app.
