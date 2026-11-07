# ADR 0029: Feature flags

Status: accepted (owner, 2026-11-06)
Date: 2026-11-06
Extends: ADR 0013 section 1 (modules), ADR 0024 (roles), ADR 0025 (audit trail), ADR 0027 (strict request decoding), ADR 0028 (pack migration version space)

## Context

A generated app today changes a feature only with a deploy.
The app cannot hide unfinished work, and it cannot stop a broken feature at run time.
Rails, Laravel, Django, and Phoenix each ship a flag library. No Go framework does.

The owner decided these points on 2026-11-06 (epic #45). They are binding:

1. Flags are declared in `[flags.<key>]` tables of `spinup.toml`. `spinup g flags` writes typed keys to `api/internal/flags/flags.gen.go`. This is the roles pattern of ADR 0024.
2. Postgres holds the runtime state.
3. Targeting is global on or off, a role list, a user list (on or off), and a percentage of signed-in users. The percentage bucket is sticky.
4. Signed-out visitors get the global value.
5. The surfaces are the app command `flags` and a role-guarded page `/admin/flags`. Pages get the evaluated `public` flags in layout data.
6. The kit API is native, in `kit/x/flags`, with the OpenFeature shape. It adds no dependency.
7. `flags` is an opt-in module that requires `auth`.

This ADR fixes the contract. The flags migration uses the pack migration space of ADR 0028. This ADR gives the SQL and leaves the path and the number to ADR 0028.

Facts about the current code that shape the design:

- `project.Parse` lists the known top-level keys in one slice. The slice has no `flags` entry, so a `[flags]` table falls into `Extra` today (`internal/project/project.go:170-173`).
- `project.Config.Validate` calls `c.AuthRoles().Validate()` at the end (`internal/project/project.go:232`). The flag validation goes next to it.
- `RenderRolesFile` builds the file as text and runs `go/format` (`internal/project/roles.go:96-112`). `RoleConst` turns `front_desk` into `FrontDesk` (`internal/project/roles.go:84-93`).
- The generator `roles` registers itself in `init()`, refuses an app without the `auth` pack, and returns one planned file in `Output.Edits` (`internal/gen/roles/roles.go:17-19`, `:26-28`, `:40-47`).
- The doctor check `roles-drift` registers with `Quick: true`, skips without the `auth` pack, compares the file bytes with the render, and scans `main.go` with `go/ast` (`internal/doctor/rolesdrift/rolesdrift.go:22`, `:36-38`, `:53-54`, `:66-88`). `audit-wired` scans `main.go` for a selector on the `audit` and `authsink` identifiers (`internal/doctor/auditwired/check.go:84-106`).
- `doctor.Context` has the fields `Root`, `Env`, `Quick`, and `Deps map[string]any`. It has no date (`internal/doctor/registry.go:58-63`). The drift runner fills `Deps` with `cli_version` and `runner` (`internal/drift/doctor.go:23`).
- The module catalog lists every embedded pack except `base`, `api`, `web`, and names that start with `_` (`internal/modules/modules.go:26-71`). A pack is a module with no Go registration.
- `modules.Select` adds the `requires` chain of a pack, and it refuses `auth` in embed mode (`internal/modules/select.go:66-68`, `:83-87`). `spinup new` refuses `auth` in embed mode too (`internal/gen/newapp/newapp.go:150`).
- `spinup module add` prints `modules.AuditNextSteps()` when `audit` is in `selection.Added` (`internal/upgrade/module.go:122-127`). `AuditNextSteps` is in `internal/modules/audit.go:54-68`.
- `main.go.tmpl` is one file with `has "<module>" .modules` blocks for the imports, the `auth.New` options, and the `app.New` options (`internal/templates/packs/api/files/api/cmd/api/main.go.tmpl:22-29`, `:78-90`, `:96-130`). It reads an extra environment variable with an anonymous struct and `config.Load` (`:72-77`). Every command, including `migrate`, runs through `a.Run(ctx, os.Args[1:])` after the whole setup (`:122`, `:131`).
- `app.WithMiddleware` appends in option order (`kit/app/options.go:49-51`). `app.New` chains the default middleware, then the option middleware (`kit/app/app.go:101`, `:122`). `httpx.Chain` makes the first middleware the outermost (`kit/httpx/middleware.go:19-24`). The telemetry handler wraps the whole chain (`kit/app/telemetry.go:17-29`), so the request span is in the context of every middleware and handler. `app.WithShutdown` hooks run in reverse order (`kit/app/options.go:64-68`). The app has no start hook.
- `auth.User` has `ID`, `Email`, and `Role` (`kit/auth/auth.go:48-55`). `auth.UserFrom` reads the user from the context (`kit/auth/auth.go:355-358`). `loadUser` maps the stored role to the effective role (`kit/auth/auth.go:444`, ADR 0024 decision 4.8).
- `Auth.RequireRole` panics for an undeclared role and answers 401 or 403 (`kit/auth/required.go:32-56`). `Auth.Roles()` returns a copy of the set, and `Roles.Admin` names the administrator role (`kit/auth/roles.go:13-20`, `:37-41`).
- `GET /api/session` answers 401 when no user is signed in (`kit/auth/handlers.go:49`, `:225-232`). The auth endpoints set `Cache-Control: no-store` (`kit/auth/handlers.go:52-60`, `:498`). `decodeCredentials` checks the media type, limits the body with `http.MaxBytesReader`, and calls `httpx.DecodeJSON` (`kit/auth/handlers.go:290-302`).
- `httpx.Problem` has no hint field. The `Try:` text is the end of `Detail` (`kit/httpx/problem.go:22-35`, ADR 0024 decision 4.7). `httpx.DecodeJSON` is strict (`kit/httpx/problem.go:105-121`). `httpx.CheckJSONBody` checks required and known fields and keeps the body readable (`kit/httpx/required.go:12-27`).
- `audit.InTx` runs `db.InTx`, then `Bind`, then the function (`kit/audit/store.go:80-90`). `Bind` runs `SELECT set_config('spinup.audit', $1, true)` (`kit/audit/store.go:62-76`). `audit.Operator()` builds the operator actor (`kit/audit/audit.go:164-173`). `authsink.ActorContext` puts the signed-in user into the context as the actor (`kit/audit/authsink/authsink.go:50-52`). `main.go.tmpl` installs it only with the audit module (`main.go.tmpl:80-83`).
- The audit migration attaches triggers in a `DO` block that checks `to_regclass` and `pg_trigger` (`internal/templates/packs/audit/files/api/db/migrations/00006_audit_events.sql:267-283`). Auth migration 00004 attaches its triggers only when `to_regprocedure('public.audit_row_change()') IS NOT NULL` (ADR 0025 decision 3.9).
- `kit/auth/usercmd.go` is the model of an operator command: a dispatcher on `args[0]`, a `flag.FlagSet` with discarded output, and the error form `<action>: <why>.\nTry: <usage>` (`kit/auth/usercmd.go:26-42`, `:130-135`, `:173-175`). `audit.Command(pool)` has the same shape (`kit/audit/command.go:46-70`).
- `apicompat` excludes the `x` directory from the stable API (`internal/apicompat/api.go:22`). The policy says so (`docs/policy/kit-compat.md:73`). `kit/api/current.txt` has no `kit/x` line.
- `kit/go.mod` has `go.opentelemetry.io/otel v1.47.0` and `go.opentelemetry.io/otel/trace v1.47.0` (`kit/go.mod:22`, `:31`). It has no OpenFeature module.
- `telemetry.Tracer` returns a tracer from the global provider (`kit/telemetry/telemetry.go:48`). The HTTP middleware is `otelhttp` with the route pattern as the span name (`kit/telemetry/http.go:18-55`).
- `kit/config.Load` reads `env` and `default` tags and parses `time.Duration` (`kit/config/config.go:14-31`, `:164-165`).
- The web pack files `app.d.ts`, `hooks.server.ts`, and `routes/+layout.server.ts` are class `scaffold`. Their sources have no `.tmpl` suffix (`internal/templates/packs/web/pack.toml:54-56`, `:64-67`, `:93-96`). `hooks.server.ts` and `+layout.server.ts` exist only when `frontend_mode != 'embed'`. The renderer runs the template engine only on a `.tmpl` source (`internal/render/render.go:93`).
- `hooks.server.ts` loads the user from `GET /api/session` and never throws (`hooks.server.ts:10-27`, `:29-39`). `handleFetch` sends the browser cookie to the Go API for `${PUBLIC_ORIGIN}/api/*` (`:50-69`). `+layout.server.ts` returns `{ user: locals.user }` (`:4`). `app.d.ts` declares `App.User` and `App.Locals { user }` (`app.d.ts:9-16`).
- `requireRole(locals, url, ...roles)` is in the auth pack file `web/src/lib/server/auth.ts:12-16`.
- `user-menu.svelte` is a `scaffold` file of the web pack, present only with `auth`, with the one prop `email` (`internal/templates/packs/web/pack.d/shell.toml:20-24`, `user-menu.svelte:8-13`). `+layout.svelte.tmpl` renders it under `has "auth" .modules` (`routes/+layout.svelte.tmpl:40-46`).
- `PlanNav` reads only the first level of `web/src/routes/` and rejects a nested route path (`internal/gen/resource/nav.go:79-94`, `:110-118`). It has no role source (ADR 0024 decision 9.3).
- The app OpenAPI document is bundled from `api/openapi/paths/*.yaml` (`internal/gen/openapi/bundle.go:25-66`). `oapi-codegen.yaml.tmpl` sets `strict-server: true`, and the generated resource server asserts `gen.StrictServerInterface` (`internal/templates/packs/api/files/api/oapi-codegen.yaml.tmpl`, `internal/gen/resource/shared.go:47`). The kit auth routes are in no fragment. A fragment for a kit route would add a method that no code implements, and the app would not build.
- `kit/MIGRATION.json` lists `manual` steps for schema changes (`kit/MIGRATION.json`).
- Docs: the build guides live in `docs/build/` with `parent: Build your app` (`docs/build/roles.md:1-5`). The app commands are in `docs/reference/app-commands.md`, the doctor checks in `docs/reference/doctor-checks.md`, and `spinup.toml` in `docs/reference/configuration.md`. The site test is `TestDocsSiteIsCurrent` (`internal/cli/docs_test.go:38`).

## Decisions

### 1. Promise and limits

1. A flag is a boolean. Code asks `flags.Enabled(ctx, appflags.NewCheckout)` and gets one answer for the whole request.
2. The answer comes from an in-process snapshot. An evaluation runs no SQL and makes no allocation.
3. A change reaches every process within the refresh interval (default 10 seconds). The process that made the change sees it at once.
4. A declared flag always has an answer. Before the first load, and when the database is down, the answer is the declared `default`.
5. The browser gets only the flags marked `public`, as booleans. It never gets a rule, a role list, a percentage, or a user list.
6. The kit, the generator, and the web pack never test for a flag key or a role name. The app owns the words.
7. A percentage applies to signed-in users only. A signed-out visitor gets the global value. Bucketing of visitors is out of scope.

### 2. Declarations in `spinup.toml` and `internal/project`

1. Each flag is one table `[flags.<key>]`:
   ```toml
   [flags.new_checkout]
   description = "The new checkout flow"
   default = false
   public = true
   owner = "ana@example.com"
   expires = "2027-03-31"
   ```
2. The keys of the table are `description` (required), `default` (bool, false when absent), `public` (bool, false when absent), `owner` (optional text), and `expires` (optional date `YYYY-MM-DD`). An unknown key in a flag table is an error.
3. Validation rules. `project.FlagDecls.Validate` checks them in this order and stops at the first failure:
   - The flag count is 0 to 64. Zero flags is valid.
   - A key matches `^[a-z][a-z0-9_]{0,47}$`. It has no double underscore and no trailing underscore. These are the role rules (`internal/project/roles.go:14`, `:52-60`) with a longer limit.
   - `description` is 1 to 200 characters, with no line break.
   - `owner` has at most 254 characters and no line break. It is free text. It is not checked as an email address.
   - `expires` is empty or parses with `time.DateOnly`. `Validate` never compares it with today. A render is a pure function (CLAUDE.md, Determinism). The expiry check is a doctor warning (decision 11.6).
   - `FlagConst(key)` values are unique, and no value is `Set`. `FlagConst` is `RoleConst` under a new name (`internal/project/roles.go:84-93`). Example: `new_checkout` gives `NewCheckout`.
   - Each error names the key and the rule, and ends with `\nTry: correct [flags.<key>] in spinup.toml and run spinup g flags.`
4. `Config` gets the field `Flags map[string]FlagDecl` with the tag `toml:"flags"`. `Parse` adds `"flags"` to the known keys (`internal/project/project.go:170`). `Config.Validate` calls `c.FlagDecls().Validate()` after the roles check (`internal/project/project.go:232`).
5. `internal/project/flags.go` holds these pure declarations. The pre-seed writes its first version, and F1 owns it after that (owner decision, 2026-11-06). It is not a shared file:
   ```go
   // FlagDecl is one [flags.<key>] table of spinup.toml.
   type FlagDecl struct {
       Key         string `toml:"-"`
       Description string `toml:"description"`
       Default     bool   `toml:"default"`
       Public      bool   `toml:"public"`
       Owner       string `toml:"owner"`
       Expires     string `toml:"expires"`
   }
   // FlagDecls is the declared flags in key order.
   type FlagDecls []FlagDecl
   func (c *Config) FlagDecls() FlagDecls   // sorted by Key; Key filled from the map key
   func (d FlagDecls) Validate() error
   func (d FlagDecl) ExpiresOn() (time.Time, bool)   // the parsed date; false when empty
   func FlagConst(key string) string
   func RenderFlagsFile(d FlagDecls) []byte
   func RenderFlagsTSFile(d FlagDecls, r AuthRoles) []byte
   const FlagsFilePath = "api/internal/flags/flags.gen.go"
   const FlagsTSFilePath = "web/src/lib/flags.gen.ts"
   ```
   `RenderFlagsFile` panics on an invalid set after `go/format`, as `RenderRolesFile` does (`internal/project/roles.go:107-110`). Callers validate first.
6. Only `spinup g flags` reads `[flags]`. No other generator or command needs the declarations in this version.

### 3. The generated files

1. The Go file is `api/internal/flags/flags.gen.go`, package `flags`. It belongs to the `flags` pack with class `managed`. The kit package has the same name, so the file imports it as `kitflags`. The exact bytes for the example of decision 2.1 are:
   ```go
   // Code generated by spinup. DO NOT EDIT.

   package flags

   import kitflags "github.com/stopyellingatme/spinup/kit/x/flags"

   const (
   	// NewCheckout is the "new_checkout" flag.
   	NewCheckout = "new_checkout"
   )

   // Set is the flag set of this app. Pass it to flags.WithDeclared.
   var Set = kitflags.Declared{
   	NewCheckout: {Description: "The new checkout flow", Default: false, Public: true, Expires: "2027-03-31"},
   }
   ```
   The keys are in sorted order. `Owner` is not in the Go file. The runtime does not use it.
2. With zero flags, the file has no `const` block. It has the comment `// No flags are declared. Add a [flags.<key>] table to spinup.toml and run spinup g flags.` above `var Set = kitflags.Declared{}`. The `flags` pack ships this file as the default. A new app has the default bytes.
3. The TypeScript file is `web/src/lib/flags.gen.ts`, class `managed`, in the `flags` pack:
   ```ts
   // Code generated by spinup. DO NOT EDIT.
   /** The public flag keys of this app. */
   export type FlagKey = 'new_checkout';
   /** The role that can open /admin/flags (admin_role in spinup.toml). */
   export const adminRole = 'manager';
   ```
   With no public flag, the type is `export type FlagKey = never;`. The default file has `never` and `'manager'`.
   The file carries `adminRole`, so the page guard and the menu link read the role from a generated file and never from a literal in the pack (ADR 0024 decision 1.10). It uses tabs and ends with one newline. Only public keys are in the union, so the browser bundle never names a private flag.
4. `spinup g flags` (package `internal/gen/flags`) renders both files from `spinup.toml`:
   - It registers in `init()` with the name `flags`.
   - It refuses positional arguments. It refuses an app whose lock has no `flags` pack: `cannot generate flags: the app has no flags module\nTry: spinup module add flags`.
   - It calls `project.Load(in.Root)`, then `config.FlagDecls().Validate()` and `config.AuthRoles().Validate()`.
   - It returns a `vfs.Plan` with the two files, class `managed`, both listed in `Output.Edits`. The runner then updates them without `--force` and keeps their lock template (`internal/gen/contract.go:52-55`).
   - It has no flags and no post-step. With no change it reports `nothing to do`, as `g roles` does.
   - `Next` is empty. The next-step line of `spinup module add flags` names the command (decision 11.5).
5. A key that you remove from `spinup.toml` and then regenerate also removes its constant. Code that uses it fails to compile. This is wanted, as for roles (ADR 0024 decision 3.8). The database row stays until `flags prune` (decision 9.6).

### 4. Kit API: package `kit/x/flags`

1. The package is under `kit/x`, so it is outside the stable API guard (`internal/apicompat/api.go:22`, `docs/policy/kit-compat.md:73`). `kit/api/current.txt` does not change. The package doc says that the API can change before v1.
2. Imports. `kit/x/flags` imports `kit/auth` (in `middleware.go` and `routes.go`), `kit/audit` (in `store.go`), `kit/httpx`, `kit/db`, `pgx`, and `go.opentelemetry.io/otel/trace`. `kit/auth` and `kit/audit` never import `kit/x/flags`. This is the direction that `kit/audit/authsink` uses for `kit/auth` (`kit/audit/authsink/authsink.go:12-13`). There is no cycle. The evaluator files (`flags.go`, `evaluate.go`, `provider.go`, `context.go`, `otel.go`) import no kit package and no database driver, so a handler test with `flagstest` stays light.
3. Types:
   ```go
   // Decl is the declaration of one flag, from the generated file.
   type Decl struct {
       Description string
       Default     bool
       Public      bool
       Expires     string // YYYY-MM-DD or empty
   }
   // Declared maps a key to its declaration. The generated Set is a Declared.
   type Declared map[string]Decl
   func (d Declared) Validate() error     // the key rules of decision 2.3, the count, and the Expires format
   func (d Declared) Keys() []string      // sorted
   func (d Decl) Expired(now time.Time) bool

   // Rule is the stored targeting of one flag.
   type Rule struct {
       Enabled    bool
       Roles      []string       // sorted, unique
       Percentage int            // 0 to 100
       Users      map[int64]bool // user ID to on or off
   }

   // Snapshot is an immutable view of every rule at one time.
   type Snapshot struct{ /* unexported: declared Declared; rules map[string]Rule; loadedAt time.Time */ }
   func NewSnapshot(declared Declared, rules map[string]Rule, loadedAt time.Time) *Snapshot
   func (s *Snapshot) Rule(key string) (Rule, bool)
   func (s *Snapshot) Keys() []string     // sorted keys of the rules
   func (s *Snapshot) LoadedAt() time.Time

   // Subject is who asks. Anonymous is true for a signed-out visitor.
   type Subject struct {
       UserID    int64
       Role      string
       Anonymous bool
   }

   // Reason says which step decided the value (OpenFeature reasons).
   type Reason string
   const (
       ReasonStatic         Reason = "static"          // the global switch decided
       ReasonDefault        Reason = "default"         // the declared default: no rule, or no snapshot
       ReasonTargetingMatch Reason = "targeting_match" // a user rule or a role rule
       ReasonSplit          Reason = "split"           // the percentage bucket
       ReasonError          Reason = "error"           // an undeclared key, or no flags scope in the context
   )

   // Resolution is the answer of one evaluation.
   type Resolution struct {
       Value   bool
       Variant string // "on" or "off"
       Reason  Reason
   }
   ```
   `NewSnapshot` copies the maps and the role slices. A `Snapshot` never changes after construction. Nothing exports its maps.
4. The OpenFeature-shaped provider:
   ```go
   // Metadata names a provider.
   type Metadata struct{ Name string }
   // Provider is the OpenFeature provider shape for booleans. An adapter can wrap it later.
   type Provider interface {
       Metadata() Metadata
       BooleanEvaluation(ctx context.Context, key string, defaultValue bool, subject Subject) Resolution
   }
   ```
   The kit has one implementation, `*Flags`:
   ```go
   type Flags struct{ /* declared Declared; snapshot atomic.Pointer[Snapshot]; logger *slog.Logger */ }
   type Option func(*options)
   func WithDeclared(d Declared) Option
   func WithLogger(l *slog.Logger) Option
   func New(opts ...Option) (*Flags, error)        // validates Declared; the error ends with a Try: line
   func (f *Flags) Metadata() Metadata             // {Name: "spinup"}
   func (f *Flags) Declared() Declared             // a copy
   func (f *Flags) Snapshot() *Snapshot            // the current snapshot, or an empty one with LoadedAt zero
   func (f *Flags) Replace(s *Snapshot)            // swaps the pointer; the store calls it
   func (f *Flags) Resolve(key string, s Subject) Resolution        // against the current snapshot; for jobs and commands
   func (f *Flags) BooleanEvaluation(ctx context.Context, key string, defaultValue bool, s Subject) Resolution
   func (f *Flags) Context(ctx context.Context, s Subject) context.Context  // installs the scope (decision 4.6)
   func (f *Flags) Middleware() httpx.Middleware   // in middleware.go (decision 6.9)
   ```
   `BooleanEvaluation` returns `defaultValue` with `ReasonError` for an undeclared key. Otherwise it is `Resolve`, plus the span event of decision 12. `New` with no `WithDeclared` has an empty set. Every key is then undeclared.
5. The pure evaluator:
   ```go
   // Resolve evaluates key for s against snap. It has no side effect.
   func Resolve(snap *Snapshot, key string, s Subject) Resolution
   ```
   It applies decision 5. The snapshot carries the declarations, so `Resolve` knows an undeclared key and the declared default.
6. The request scope. The middleware stores one unexported `scope{provider *Flags; snapshot *Snapshot; subject Subject}` in the request context. One request keeps one snapshot pointer, so every evaluation of the request sees one state (decision 6.8).
   ```go
   // Evaluate evaluates key for the scope in ctx. It adds the span event of decision 12.
   func Evaluate(ctx context.Context, key string) Resolution
   // Enabled is Evaluate(ctx, key).Value.
   func Enabled(ctx context.Context, key string) bool
   // SubjectFrom returns the subject of the scope in ctx. ok is false without a scope.
   func SubjectFrom(ctx context.Context) (s Subject, ok bool)
   ```
   Without a scope, `Evaluate` returns `{Value: false, Variant: "off", Reason: ReasonError}`. It does not panic and does not log. The doc comment says: install `Middleware` after the auth middleware, or call `Flags.Context` in a job or a command. A job gets its scope with `flagsProvider.Context(ctx, flags.Subject{Anonymous: true})`, or evaluates with `flagsProvider.Resolve`.
7. The subject of a request comes from `auth.UserFrom(r.Context())` in `Middleware` (`kit/auth/auth.go:355-358`). With a user, the subject is `{UserID: u.ID, Role: u.Role}`. Without a user, it is `{Anonymous: true}`. `u.Role` is the effective role of ADR 0024 decision 4.8, so an empty stored role reads as `default_role`.
8. `flagstest` (package `kit/x/flags/flagstest`):
   ```go
   // Override forces key to value for every evaluation in this process until the test ends.
   // The reason of an overridden evaluation is ReasonStatic. An undeclared key works.
   func Override(t testing.TB, key string, value bool)
   // Context returns ctx with a scope whose snapshot holds one global rule for each key,
   // an anonymous subject, and a provider with the keys declared. For handler tests.
   func Context(ctx context.Context, values map[string]bool) context.Context
   // ContextAs is Context with a signed-in subject.
   func ContextAs(ctx context.Context, values map[string]bool, s flags.Subject) context.Context
   ```
   `Override` writes a package-level table in `kit/x/flags` behind a mutex and an `atomic.Bool` fast path. `Evaluate`, `Enabled`, `Resolve`, and `BooleanEvaluation` consult it first when the fast path is set. `t.Cleanup` removes the entry. The doc says: do not call `t.Parallel` in a test that uses `Override` with a key that another test overrides. The fast path keeps `Enabled` at zero allocations when no override is active.

### 5. Precedence and the bucket

1. `Resolve` applies these steps in order and stops at the first that decides:
   | Step | Condition | Value | Reason |
   |---|---|---|---|
   | 0 | The key is not declared | `false` | `error` |
   | 0 | The key has no rule in the snapshot, or the snapshot is empty | `Decl.Default` | `default` |
   | 1 | The subject is signed in and `Rule.Users[UserID]` exists | that value | `targeting_match` |
   | 2 | `Rule.Enabled` is true | `true` | `static` |
   | 3 | The subject is signed in and `Rule.Roles` contains `Subject.Role` | `true` | `targeting_match` |
   | 4 | The subject is signed in, `Rule.Percentage > 0`, and `bucket(key, UserID) < Rule.Percentage` | `true` | `split` |
   | 5 | Otherwise | `false` | `static` |
   A user rule `off` beats a global `on`. A specific rule beats a general one.
2. The bucket is `bucket(key, userID) = fnv32a(key + ":" + strconv.FormatInt(userID, 10)) % 100`, with `hash/fnv` `New32a`. The key is in the hash, so each flag picks other users. The percentage is not in the hash, so a raise from 5 to 20 keeps every user who was on and adds users. A lower percentage removes users from the top of the bucket range only.
3. Step 5 has the reason `static` and not `default`. The stored global switch is off, and the stored value decided. `default` means that no row decided.
4. An anonymous subject skips steps 1, 3, and 4. It gets the global value with the reason `static`.
5. A stored role that the app no longer declares stays in `Rule.Roles`. Step 3 is an exact string match, so a user whose stored role is that same undeclared text still matches (ADR 0024 decision 4.8 keeps a non-empty undeclared role as it is). `Store.SetRoles` refuses an undeclared role (decision 6.5), so the list cannot grow. `flags list` and `GET /api/admin/flags` mark such a role as undeclared (decisions 8.4 and 9.2). This is the accepted risk of ADR 0024 decision 7.10, applied to flags. The docs tell the owner to run `flags set <key> --roles ...` before a role rename.
6. An empty `Subject.Role` matches no role, because `SetRoles` never stores an empty name.
7. `Variant` is `"on"` for `true` and `"off"` for `false`.

### 6. Schema, sync, snapshot, and refresh

1. The flags migration uses the pack migration space of ADR 0028. ADR 0028 names the directory, the file name, and the number. The file is a pack file of the `flags` pack with class `seed`. Its SQL is:
   ```sql
   -- +goose Up
   CREATE TABLE feature_flags (
       key text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]{0,47}$'),
       enabled boolean NOT NULL DEFAULT false,
       roles text[] NOT NULL DEFAULT '{}',
       percentage smallint NOT NULL DEFAULT 0 CHECK (percentage BETWEEN 0 AND 100),
       updated_at timestamptz NOT NULL DEFAULT now()
   );

   CREATE TABLE feature_flag_users (
       flag_key text NOT NULL REFERENCES feature_flags(key) ON DELETE CASCADE,
       user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
       enabled boolean NOT NULL,
       updated_at timestamptz NOT NULL DEFAULT now(),
       PRIMARY KEY (flag_key, user_id)
   );

   -- The audit module records flag changes. The block acts only when the audit functions
   -- exist (the audit migration ran first). When the audit module comes later, the doctor
   -- check flags-wired prints the attach statements (ADR 0029 decision 11.7).
   -- +goose StatementBegin
   DO $$
   BEGIN
       IF to_regprocedure('public.audit_row_change()') IS NOT NULL THEN
           IF NOT EXISTS (SELECT 1 FROM pg_trigger
                   WHERE tgrelid = to_regclass('public.feature_flags') AND tgname = 'feature_flags_audit') THEN
               CREATE TRIGGER feature_flags_audit AFTER INSERT OR UPDATE OR DELETE ON public.feature_flags
                   FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'key');
           END IF;
           IF NOT EXISTS (SELECT 1 FROM pg_trigger
                   WHERE tgrelid = to_regclass('public.feature_flags') AND tgname = 'feature_flags_audit_truncate') THEN
               CREATE TRIGGER feature_flags_audit_truncate AFTER TRUNCATE ON public.feature_flags
                   FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
           END IF;
           IF NOT EXISTS (SELECT 1 FROM pg_trigger
                   WHERE tgrelid = to_regclass('public.feature_flag_users') AND tgname = 'feature_flag_users_audit') THEN
               CREATE TRIGGER feature_flag_users_audit AFTER INSERT OR UPDATE OR DELETE ON public.feature_flag_users
                   FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'flag_key,user_id');
           END IF;
           IF NOT EXISTS (SELECT 1 FROM pg_trigger
                   WHERE tgrelid = to_regclass('public.feature_flag_users') AND tgname = 'feature_flag_users_audit_truncate') THEN
               CREATE TRIGGER feature_flag_users_audit_truncate AFTER TRUNCATE ON public.feature_flag_users
                   FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
           END IF;
       END IF;
   END
   $$;
   -- +goose StatementEnd

   -- +goose Down
   DROP TABLE feature_flag_users;
   DROP TABLE feature_flags;
   ```
   The `DO` block follows `00006_audit_events.sql:267-283` and ADR 0025 decision 3.9. The migration text is the same with and without the audit module, so an app without `audit` renders the same bytes. `DROP TABLE` drops the triggers with the tables, so the Down step needs no `DROP TRIGGER`. The owner column argument is empty: a user rule has a target, not an owner. The key of `feature_flag_users` is composite, so `record_id` is `new_checkout:42` (ADR 0025 decision 3.5). The migration needs the table `users` from auth 00002. ADR 0028 orders the pack space after the auth migrations.
2. The store:
   ```go
   type Store struct{ /* pool *pgxpool.Pool; flags *Flags; roles auth.Roles; logger; stop, done chan struct{} */ }
   type StoreOption func(*storeOptions)
   func NewStore(pool *pgxpool.Pool, f *Flags, roles auth.Roles, opts ...StoreOption) *Store
   func WithStoreLogger(l *slog.Logger) StoreOption
   ```
   `NewStore` validates nothing against the database and starts nothing. A nil pool or a nil `*Flags` panics at construction, so the error appears at startup.
3. Sync:
   ```go
   // Sync inserts one row for each declared key that has no row, with enabled = Decl.Default.
   // It never changes an existing row. It returns the stale keys: rows with no declaration, sorted.
   func (s *Store) Sync(ctx context.Context) (stale []string, err error)
   ```
   It runs one transaction with `INSERT INTO feature_flags (key, enabled) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING` for each key in sorted order, then `SELECT key FROM feature_flags ORDER BY key` for the stale report. It logs each stale key at warn level: `flags: the row "x" has no declaration. Try: run "flags prune --yes" to delete it, or declare [flags.x] in spinup.toml.`
4. Load and refresh:
   ```go
   // Load reads both tables in one REPEATABLE READ, READ ONLY transaction and returns the snapshot.
   func (s *Store) Load(ctx context.Context) (*Snapshot, error)
   // Refresh is Load, then Flags.Replace.
   func (s *Store) Refresh(ctx context.Context) error
   ```
   `Load` uses `pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})`, so the two `SELECT` statements see one state. `LoadedAt` is the database `now()` of that transaction, not the process clock. The snapshot holds the rules of every row, stale rows included, so `flags list` and the admin list can show them. Evaluation of a stale key returns `false` with the reason `error`, because the key is not declared (decision 5.1, step 0). The generated constant is gone, so code cannot name the key. `flags prune` deletes the row.
5. Writes. Each method validates, then runs one transaction through `audit.InTx` (decision 7), then calls `Refresh`:
   ```go
   func (s *Store) Enable(ctx context.Context, key string) error
   func (s *Store) Disable(ctx context.Context, key string) error
   func (s *Store) SetRoles(ctx context.Context, key string, roles []string) error
   func (s *Store) SetPercentage(ctx context.Context, key string, percentage int) error
   func (s *Store) SetRule(ctx context.Context, key string, r Rule) error   // enabled, roles, and percentage at once; Users is ignored
   func (s *Store) SetUser(ctx context.Context, key, email string, enabled bool) error
   func (s *Store) ClearUser(ctx context.Context, key, email string) error
   func (s *Store) Prune(ctx context.Context) (deleted []string, err error)
   func (s *Store) List(ctx context.Context, now time.Time) ([]Entry, error)
   ```
   - An undeclared key returns `ErrUnknownKey`: `flags: the flag "x" is not declared. Declared flags: a, b.\nTry: add [flags.x] to spinup.toml and run spinup g flags.` A stale row is undeclared, so no write reaches it. Only `Prune` touches stale rows.
   - `SetRoles` sorts and deduplicates. A name that `s.roles.Valid` rejects (`kit/auth/roles.go:44`) returns `ErrUnknownRole`: `flags: the role "x" is not declared. Declared roles: manager, staff.\nTry: use one of the declared roles, or add the role to [auth] in spinup.toml and run spinup g roles.`
   - `SetPercentage` outside 0 to 100 returns `ErrPercentage`: `flags: the percentage 120 is not from 0 to 100.\nTry: use a whole number from 0 to 100.`
   - `SetUser` and `ClearUser` normalize the email as auth does: trim, then lower-case (`kit/auth/handlers.go:311`). They run `SELECT id FROM users WHERE email = $1` inside the transaction. No row returns `ErrUserNotFound`: `flags: no account has this email.\nTry: run "user list" to see the accounts.` `SetUser` upserts with `ON CONFLICT (flag_key, user_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`. `ClearUser` of a missing rule succeeds.
   - Every update sets `updated_at = now()`.
   - `Prune` deletes every row whose key is not declared. The cascade deletes its user rules. It returns the deleted keys, sorted.
   - A failed `Refresh` after a successful write logs at warn and returns nil. The write is committed. The runner loads it on the next tick.
6. `Entry` is one row for `List`, the admin route, and the command:
   ```go
   type Entry struct {
       Key         string     `json:"key"`
       Description string     `json:"description"`
       Default     bool       `json:"default"`
       Public      bool       `json:"public"`
       Expires     string     `json:"expires,omitempty"`
       Enabled     bool       `json:"enabled"`
       Roles       []string   `json:"roles"`
       Percentage  int        `json:"percentage"`
       Users       []UserRule `json:"users"`
       Stale       bool       `json:"stale"`    // no declaration
       Expired     bool       `json:"expired"`  // Expires is before now
       UpdatedAt   time.Time  `json:"updated_at"`
   }
   type UserRule struct {
       UserID  int64  `json:"user_id"`
       Email   string `json:"email"`
       Enabled bool   `json:"enabled"`
   }
   ```
   `Roles` and `Users` are never nil in JSON (`[]`), as the list handlers of ADR 0027 decision 4 do. `Users` joins `users` for the email and sorts by email. A declared key with no row (before `Sync`) appears with its defaults and `UpdatedAt` zero.
7. The runner:
   ```go
   // Start runs Sync and Refresh once, then refreshes every interval until Close or ctx ends.
   // A database failure of the first Sync or Refresh logs at warn and does not fail Start.
   // The snapshot stays empty, so every flag answers its declared default (reason default).
   func (s *Store) Start(ctx context.Context, interval time.Duration) error
   // Close stops the runner. It fits app.WithShutdown.
   func (s *Store) Close(ctx context.Context) error
   // Ready reports the last refresh error. It fits app.WithReadiness.
   func (s *Store) Ready(ctx context.Context) error
   ```
   - `Start` returns an error only for a configuration fault: an interval below 1 second (`flags: the refresh interval 500ms is below 1s.\nTry: set FLAGS_REFRESH to 1s or more.`), or a second call.
   - The interval comes from `FLAGS_REFRESH`. `main.go` reads it with `config.Load` and the tag `env:"FLAGS_REFRESH" default:"10s"` (the pattern of `main.go.tmpl:72-77`). The kit does not read the environment.
   - Each tick runs `Refresh` with a timeout of `min(interval, 5s)`. A failure logs at warn: `flags: cannot refresh the snapshot: <err>. The last snapshot stays in use.` The last snapshot stays. The next tick tries again. A success after a failure logs at info once.
   - Why `Start` does not fail on a database error: `main.go` runs the same setup before every command, including `migrate` (`main.go.tmpl:122`, `:131`). A hard failure would stop `migrate` on a new database. Deploy migrates before it starts the new binary (ADR 0024 decision 11.11), so a served app finds its tables. `Ready` makes an unmigrated schema visible: `/readyz` answers 503 with `"flags": "fail"` until the first successful load.
   - This corrects issue #50, which said "fails to start on an error".
8. One request sees one snapshot. `Flags.Replace` swaps an `atomic.Pointer[Snapshot]`. `Middleware` reads the pointer once at the start of the request and stores it in the scope. Later evaluations in the request read the scope, not the pointer. A swap during the request does not change the answer.
9. The middleware:
   ```go
   // Middleware installs the scope of decision 4.6: the current snapshot and the subject of the
   // kit auth user. Register it after the auth middleware.
   func (f *Flags) Middleware() httpx.Middleware
   ```
   `app.WithMiddleware` keeps option order, and `httpx.Chain` makes the first option outermost (`kit/app/options.go:49-51`, `kit/httpx/middleware.go:19-24`). `app.WithMiddleware(authn.Middleware())` first, then `app.WithMiddleware(flagsProvider.Middleware())`, gives the flags middleware the user. Without the auth middleware, every subject is anonymous. The doctor check `flags-wired` checks the order (decision 11.7).

### 7. Audit interaction

1. Every write of `Store` runs through `audit.InTx` (`kit/audit/store.go:80-90`). `InTx` binds the actor of the context with `set_config('spinup.audit', ..., true)` (`kit/audit/store.go:74`). A transaction-local custom setting works on a database without the audit schema. The statement costs one round trip and does nothing else. So the store does not need to know whether the audit module is on, and `main.go` passes no audit switch to `NewStore`.
2. With the audit module, the triggers of decision 6.1 write one `audit_events` row for each change of `feature_flags` and `feature_flag_users`. The actor is:
   - the signed-in administrator for a request through the admin routes. `main.go` installs `auth.WithActorContext(authsink.ActorContext)` with the audit module (`main.go.tmpl:80-83`), so the context holds the user as the actor when the handler runs.
   - the operator for the command. `Command` wraps the context with `audit.WithActor(ctx, audit.Operator())` (`kit/audit/audit.go:164-173`), so the event has `kind = operator`, `source = cli`, and `operator:<login>`.
3. Without the audit module, no trigger exists. The `set_config` statement runs and no row is written. `flags list` and the admin page show `updated_at` only. There is no change history.
4. When the audit module is added after the flags module, the audit migration does not attach the flag triggers. It knows only `users` and `user_identities` (ADR 0025 decision 3.9). The doctor check `flags-wired` warns and prints the attach statements (decision 11.7). The retrofit `spinup g audit <table>` is not used here. It assumes the key column `id`, and `feature_flags` has the key `key`.
5. The flags store writes no explicit event with `audit.Record`. The row events hold the old and new values. `flags prune` writes nothing special: the trigger records each `delete`.

### 8. HTTP routes

1. The routes are kit routes, like the auth routes (`kit/auth/handlers.go:44-50`). They are not in the app OpenAPI document. The document feeds oapi-codegen with `strict-server: true`, and the generated resource server asserts `gen.StrictServerInterface` (`internal/gen/resource/shared.go:47`). A fragment for a route that no generated code implements would stop the app build. The auth routes set the precedent: `web/src/lib/server/auth.ts` calls them with typed helpers and not through the generated client (`auth.ts:59-83`). The web side does the same (decision 10.5). This corrects issue #51, which asked for an OpenAPI fragment.
2. Registration:
   ```go
   // Routes registers the flag routes. a guards the admin routes with a.RequireRole(a.Roles().Admin).
   func (s *Store) Routes(a *auth.Auth) func(mux *http.ServeMux)
   ```
   `main.go` passes it to `app.WithRoutes`. The routes need both middlewares.
3. `GET /api/flags`, for every visitor:
   - 200 `{"flags":{"new_checkout":true}}`. The map holds every declared key with `Public: true`, evaluated with `flags.Evaluate(r.Context(), key)` for the subject of the request. The order of the keys is sorted. With no public flag the body is `{"flags":{}}`.
   - `Cache-Control: no-store`. The answer depends on the cookie.
   - It never answers 401. A signed-out visitor gets the global values (decision 5.4).
   - It never returns a rule. Only the booleans leave the server.
   - Why a separate route and not a field of `GET /api/session`: the session route answers 401 to a signed-out visitor (`kit/auth/handlers.go:225-229`), and public flags must reach that visitor. The session body `{"user":...}` is a stable `kit/auth` contract in `current.txt`, and `kit/x` is unstable. `kit/auth` must not import `kit/x/flags` (decision 4.2). `hooks.server.ts` runs the two requests in parallel (decision 10.1), so the page pays no extra latency.
   - Each public key adds one span event (decision 12). The count is at most 64.
4. The admin routes. Each handler is wrapped in `a.RequireRole(a.Roles().Admin)` (`kit/auth/required.go:32-56`). It answers 401 for no user and 403 for a wrong role, with the auth texts and `no-store`. Every admin answer sets `Cache-Control: no-store`.
   - `GET /api/admin/flags` → 200 `{"flags":[Entry, ...],"roles":["manager","staff"],"admin_role":"manager"}`. `flags` is sorted by key and holds stale rows too. `roles` is `a.Roles().Names`, so the page renders the role checkboxes with no role in TypeScript. `Entry` is decision 6.6. A role in `Entry.Roles` that is not in `roles` is undeclared. The page marks it.
   - `PUT /api/admin/flags/{key}` with the body `{"enabled":false,"roles":["manager"],"percentage":50}`. All three fields are required. `PUT` replaces the rule. The handler checks `Content-Type: application/json` (415 otherwise, as `decodeCredentials` does), limits the body to 16 KiB with `http.MaxBytesReader`, runs `httpx.CheckJSONBody(r, httpx.BodyRules{Required: []string{"enabled", "roles", "percentage"}, Known: []string{"enabled", "roles", "percentage"}})`, then `httpx.DecodeJSON` (ADR 0027 decisions 2 and 3). It calls `Store.SetRule`. Answers: 200 with the `Entry`; 404 for an undeclared or stale key; 422 with field errors; 400 and 413 from the decoders.
   - `PUT /api/admin/flags/{key}/users/{email}` with the body `{"enabled":true}` (required, known: `enabled`). It calls `Store.SetUser`. Answers: 200 with the `Entry`; 404 for the key; 422 with the field error `email` when no account has the email.
   - `DELETE /api/admin/flags/{key}/users/{email}`. It calls `Store.ClearUser`. Answers: 204; 404 for the key. Clearing a rule that does not exist answers 204.
   - `{email}` is URL-escaped in the path. `r.PathValue("email")` returns it decoded. The handler normalizes it (decision 6.5).
   - There is no prune route and no route to create a flag. Declarations live in `spinup.toml`.
5. Problem answers. All use `application/problem+json` through `httpx.WriteProblem` (`kit/httpx/problem.go:66-86`):
   - 404: `Detail: The flag "x" is not declared. Try: add [flags.x] to spinup.toml and run spinup g flags.`
   - 422 roles: `Detail: The request has invalid fields.`, `Errors: [{"field":"roles","message":"The role \"x\" is not declared. Use one of: manager, staff."}]`.
   - 422 percentage: `Errors: [{"field":"percentage","message":"Use a whole number from 0 to 100."}]`.
   - 422 email: `Errors: [{"field":"email","message":"No account has this email address."}]`.
   - 500 for a database error, with the generic problem. The handler logs the error with the request ID.
6. CSRF. The auth middleware rejects unsafe cross-origin requests for every route behind it (`kit/auth/auth.go:372`). The admin routes are behind it, so the browser form actions through `handleFetch` pass and a cross-site `PUT` fails with 403.
7. A write answers with the state after `Refresh`. The process that answers sees the new value at once. Another replica sees it on its next tick.

### 9. The app command `flags`

1. `func (s *Store) Command() func(ctx context.Context, args []string) error`. `main.go` registers it with `app.WithCommand("flags", flagStore.Command())`. The model is `kit/auth/usercmd.go`. The command never prompts.
2. Actions and output:
   - `flags list [--json]`. One line for each entry, sorted by key, through `text/tabwriter`: `KEY  STATE  ROLES  PERCENT  USERS  NOTES`. Example:
     ```
     KEY           STATE  ROLES            PERCENT  USERS  NOTES
     new_checkout  on     manager          50       2      public
     old_banner    off    -                0        0      expired 2026-10-01
     beta_report   off    cook(undeclared) 0        1      stale
     ```
     `NOTES` lists, in this order and separated by a comma: `public`, `default on`, `expires YYYY-MM-DD` or `expired YYYY-MM-DD`, `stale`. `--json` prints the `Entry` list of decision 6.6 and the declared roles.
   - `flags enable <key>` and `flags disable <key>`. Output: `updated new_checkout	on`.
   - `flags set <key> [--roles a,b] [--percent N]`. At least one of the two flags is required. `--roles ""` clears the roles. `--percent 0` clears the percentage. Output: `updated new_checkout	roles=manager percent=50`.
   - `flags user <key> --email E (--on | --off | --clear)`. Exactly one of the three is required. Output: `updated new_checkout	ana@example.com on`.
   - `flags prune --yes`. It deletes the stale rows and prints `deleted <key>` for each. Without `--yes`: `flags prune: --yes is required, because the command deletes rows.\nTry: flags prune --yes`. With nothing to delete: `nothing to prune`.
3. The actor is `audit.Operator()` (decision 7.2). There is no `--reason` flag in this version.
4. Errors follow the form of `userCommandError` (`kit/auth/usercmd.go:173-175`): `flags <action>: <why>.\nTry: <usage>`. The usage string is `flags list [--json] | flags enable KEY | flags disable KEY | flags set KEY [--roles a,b] [--percent N] | flags user KEY --email E (--on|--off|--clear) | flags prune --yes`. A store error (`ErrUnknownKey`, `ErrUnknownRole`, `ErrPercentage`, `ErrUserNotFound`) passes through with its own `Try:` line, wrapped as `flags <action>: <err>`.
5. The command reads the database through the pool of `main.go`. It needs no web server. `Start` has already run, so the entries are synced.
6. `flags prune` is the only path that deletes a row. Declared rows are never deleted.

### 10. Web

1. `hooks.server.ts` (web pack, scaffold, becomes `hooks.server.ts.tmpl`) gets a block under `has "flags" .modules`:
   - `loadFlags(cookie)` fetches `${api}/api/flags` with the `cookie` header when there is one, and `accept: application/json`. It returns the `flags` map, or `{}` on any error, with `console.warn`. It never throws (the shape of `loadUser`, `hooks.server.ts:10-27`).
   - `handle` runs `Promise.all([loadUser(cookie), loadFlags(cookie)])` and sets `event.locals.user` and `event.locals.flags`. For `/healthz` both are skipped: `user` is `null` and `flags` is `{}`.
   - A failed flag read never breaks a page. The page renders with every public flag off.
2. `app.d.ts` (scaffold, becomes `app.d.ts.tmpl`): under `has "flags"`, `Locals` gets `flags: Record<string, boolean>;`. The key type stays `string` here, because a global `d.ts` is simpler without an import. The helper narrows the key.
3. `routes/+layout.server.ts` (scaffold, becomes `+layout.server.ts.tmpl`): under `has "flags"`, the load returns `{ user: locals.user, flags: locals.flags }`. Every page gets `data.flags`. Pages never evaluate a rule in the browser. The SSR render and the browser agree, because both read the same data.
4. The helper `web/src/lib/flags.ts` (flags pack, class `managed`):
   ```ts
   // Managed by spinup: `spinup upgrade` keeps this file current.
   import type { FlagKey } from '#lib/flags.gen.js';

   /** flag reads a public flag from page or layout data. A missing key is off. */
   export function flag(data: { flags?: Record<string, boolean> }, key: FlagKey): boolean {
   	return data.flags?.[key] === true;
   }
   ```
   A page writes `{#if flag(data, 'new_checkout')}`. A private key does not compile, because it is not in `FlagKey`.
5. The server helper `web/src/lib/server/flags.ts` (flags pack, class `managed`) holds the typed calls of the admin page: `listFlags(fetch)`, `putRule(fetch, key, rule)`, `putUser(fetch, key, email, enabled)`, and `clearUser(fetch, key, email)`. Each takes the `fetch` of the load or action and calls `${__PUBLIC_ORIGIN__}/api/admin/flags...`, so `handleFetch` sends the cookie. Each returns `{ ok: true, data }` or `{ ok: false, status, errors }` from the problem body, in the shape of `postAuth` (`auth.ts:59-83`). The TypeScript types `FlagEntry`, `UserRule`, and `FlagsAdmin` mirror decision 6.6 and 8.4.
6. The page `/admin/flags` (flags pack, three `scaffold` files: `web/src/routes/admin/flags/+page.server.ts`, `+page.svelte`, `schema.ts`):
   - The load calls `requireRole(locals, url, adminRole)` with `adminRole` from `#lib/flags.gen.js`, then `listFlags`. A failure shows `error(status, message)`.
   - The actions are `?/rule` (key, enabled, roles, percentage), `?/user` (key, email, enabled), and `?/clearUser` (key, email). Each action calls `requireRole` first. Each uses `sveltekit-superforms` with the `zod4` adapter and `superValidate`, `fail(400, { form })`, `setError` for a field error of the API, and `message` for a form error (ADR 0015 section 5). On success the action redirects to `/admin/flags?saved=1`, and the page shows `toast.success`.
   - The page shows one table: key, description, a `Switch` for `enabled` that posts `?/rule` with the current roles and percentage, one `Checkbox` for each declared role, a number input for the percentage, the user rules with a clear button, a form to add a user rule by email, and badges `public`, `stale`, `expired`, and `undeclared` on a role. It uses the existing `ui` components and `field.svelte`. It needs no JavaScript to post.
   - A stale row shows no controls. The page tells the owner to run `flags prune --yes`.
   - `PlanNav` never lists this page. It reads only the first level of `web/src/routes/` and rejects a nested path (`internal/gen/resource/nav.go:79-94`, `:110-118`). `nav.gen.ts` does not change.
7. Navigation. The link `Feature flags` is in the user menu, under `Account`. `user-menu.svelte` (web pack, scaffold, becomes `user-menu.svelte.tmpl`) gets an optional prop `links: { href: string; label: string }[]` under `has "flags"`, default `[]`. `+layout.svelte.tmpl` passes `links={data.user.role === adminRole ? [{ href: '/admin/flags', label: 'Feature flags' }] : []}` under `has "flags"`, with `adminRole` imported from `#lib/flags.gen.js`. The sidebar is not touched. The server guard `requireRole` is the enforcement. The menu condition is cosmetic (ADR 0024 decision 9.4).
8. Embed mode. The flags module requires `auth`. `spinup new` and `spinup module add` refuse `auth` in embed mode (`internal/gen/newapp/newapp.go:150`, `internal/modules/select.go:66-68`). So an embed app cannot have the flags module, and the web pack files with `when = "frontend_mode != 'embed'"` are always present when `flags` is. This corrects issue #52: there is no embed case to document, except one line that says the module needs `--frontend-mode node`.
9. Byte identity. Every change to a web pack file sits under `has "flags" .modules`. The conversion of a source to `.tmpl` changes no byte of an app without the module. The goldens of the `web` and `auth` packs prove it.

### 11. Module, wiring, and doctor checks

1. The module is the pack `internal/templates/packs/flags` with `requires = ["auth"]`. A pack is a module with no Go registration (ADR 0013 section 1, `internal/modules/modules.go:26-71`). `spinup module add flags` adds `auth` and `web` through the requires chain (`internal/modules/select.go:83-87`). It has no `[[questions]]`.
2. The pack files and their classes:
   | Path | Class | Owner unit |
   |---|---|---|
   | the flags migration (path per ADR 0028) | `seed` | F3 |
   | `api/internal/flags/flags.gen.go` | `managed` | F0 (default bytes), F1 renders |
   | `web/src/lib/flags.gen.ts` | `managed` | F0 (default bytes), F1 renders |
   | `web/src/lib/flags.ts` | `managed` | F6 |
   | `web/src/lib/server/flags.ts` | `managed` | F6 |
   | `web/src/routes/admin/flags/+page.server.ts` | `scaffold` | F6 |
   | `web/src/routes/admin/flags/+page.svelte` | `scaffold` | F6 |
   | `web/src/routes/admin/flags/schema.ts` | `scaffold` | F6 |
   The pack has no `spinup.toml` example file, because `spinup.toml` is app-owned (ADR 0024 decision 11.6). The next steps print the example (decision 11.5).
3. The `api` pack `main.go.tmpl` gets these blocks under `has "flags" .modules`:
   - imports: `"time"`, `appflags "[[ .module ]]/api/internal/flags"`, and `"github.com/stopyellingatme/spinup/kit/x/flags"`
   - after `auth.New` and before `app.New`:
     ```go
     var flagsEnv struct {
      Refresh time.Duration `env:"FLAGS_REFRESH" default:"10s"`
     }
     if err := config.Load(&flagsEnv); err != nil {
      return err
     }
     flagsProvider, err := flags.New(flags.WithDeclared(appflags.Set))
     if err != nil {
      return err
     }
     flagStore := flags.NewStore(pool, flagsProvider, roles.Set)
     if err := flagStore.Start(ctx, flagsEnv.Refresh); err != nil {
      return err
     }
     ```
   - in `app.New`, directly after `app.WithMiddleware(authn.Middleware())`: `app.WithMiddleware(flagsProvider.Middleware()),`
   - in `app.New`, after the auth routes: `app.WithRoutes(flagStore.Routes(authn)),`, `app.WithCommand("flags", flagStore.Command()),`, `app.WithReadiness("flags", flagStore.Ready),`
   - in `app.New`, after `app.WithShutdown(authn.Close)`: `app.WithShutdown(flagStore.Close),`
   The order of the middleware options is the contract of decision 6.9.
4. `main.go` is `scaffold` (ADR 0024 decision 11.4). An upgrade can leave an edited `main.go` without these lines. Then the app builds and runs, `flags.Enabled` returns `false` with the reason `error`, and `flags-wired` warns.
5. `spinup module add flags` prints `modules.FlagsNextSteps()` (new file `internal/modules/flags.go`) in `internal/upgrade/module.go`, next to the audit block (`internal/upgrade/module.go:122-127`). The text:
   ```
   Next: declare a flag in spinup.toml, for example:
    [flags.new_checkout]
    description = "The new checkout flow"
    public = true
   Next: run spinup g flags to write api/internal/flags/flags.gen.go and web/src/lib/flags.gen.ts.
   Next: in api/cmd/api/main.go, import:
    appflags "<module>/api/internal/flags"
    "github.com/stopyellingatme/spinup/kit/x/flags"
   Next: before app.New, read FLAGS_REFRESH, build flags.New(flags.WithDeclared(appflags.Set)),
    flags.NewStore(pool, flagsProvider, roles.Set), and call flagStore.Start(ctx, refresh). See docs/build/flags.md.
   Next: add these options to app.New(...), after app.WithMiddleware(authn.Middleware()):
    app.WithMiddleware(flagsProvider.Middleware()),
    app.WithRoutes(flagStore.Routes(authn)),
    app.WithCommand("flags", flagStore.Command()),
    app.WithReadiness("flags", flagStore.Ready),
    app.WithShutdown(flagStore.Close),
   Next: in web/src/hooks.server.ts, load GET /api/flags next to the session and set locals.flags.
    In web/src/app.d.ts add flags: Record<string, boolean> to Locals. In web/src/routes/+layout.server.ts return flags.
    In web/src/routes/+layout.svelte pass the Feature flags link to UserMenu. See docs/build/flags.md.
   Next: run spinup db migrate up before starting the new binary.
   Next: run spinup doctor and inspect the flags-drift and flags-wired checks.
   ```
   `<module>` is the Go module of the app. The web scaffold files (`hooks.server.ts`, `app.d.ts`, `+layout.server.ts`, `+layout.svelte`, `user-menu.svelte`) merge three-way on `module add` and `upgrade`. An edited file can conflict. The next steps name each edit, and the docs show the blocks.
6. The doctor check `flags-drift` (package `internal/doctor/flagsdrift`, scope Local, `Quick: true`):
   - `skip` outside a project, or when the lock has no `flags` pack.
   - `warn` when `spinup.toml` does not parse or `FlagDecls().Validate()` fails: `spinup.toml has invalid flags.`, `Try: correct [flags] in spinup.toml and run spinup g flags`.
   - `warn` when `api/internal/flags/flags.gen.go` or `web/src/lib/flags.gen.ts` is missing or differs from the render: `<path> does not match [flags] in spinup.toml.`, `Try: spinup g flags`. It names the first path that differs.
   - `warn` when a flag has `expires` before today: `2 flags are past their expires date: old_banner (2026-10-01), beta_report (2026-10-15).`, `Try: remove each [flags.<key>] table and its uses from the code, run spinup g flags, then flags prune --yes`.
   - Today comes from `c.Deps["now"]` as a `time.Time` when the runner sets it. Otherwise it is `time.Now().UTC()`. `doctor.Context` has no date field (`internal/doctor/registry.go:58-63`), and this version does not add one. Tests set `Deps["now"]`. The comparison uses the UTC date only.
   - `ok`: `generated flags match [flags] in spinup.toml`.
7. The doctor check `flags-wired` (package `internal/doctor/flagswired`, scope Local, `Quick: true`):
   - `skip` outside a project, or when the lock has no `flags` pack.
   - It parses `api/cmd/api/main.go` with `go/parser` and `parser.SkipObjectResolution` (the pattern of `rolesdrift.go:66-88` and `auditwired.go:84-106`). It finds the identifier that an `AssignStmt` binds from a call `flags.New(...)`, and the identifier bound from `flags.NewStore(...)`. It then checks these calls:
     - `flags.WithDeclared(` inside `flags.New(`
     - `<store>.Start(`
     - `WithMiddleware(<provider>.Middleware())`
     - `WithMiddleware(authn.Middleware())` before it, by position in the `app.New` argument list. The auth identifier is the one bound from `auth.New(`.
     - `WithRoutes(<store>.Routes(`
     - `WithCommand("flags", `
     - `WithShutdown(<store>.Close)`
     Each missing call gives one `warn`. The message names the call, and `Try:` gives the exact line from decision 11.3. A wrong middleware order gives `api/cmd/api/main.go registers the flags middleware before the auth middleware, so every subject is anonymous.`, `Try: move app.WithMiddleware(flagsProvider.Middleware()) after app.WithMiddleware(authn.Middleware())`. The check reports the first problem it finds.
   - When the lock also has the `audit` pack: it reads `api/db/migrations/*.sql` (and the pack space directory of ADR 0028) and looks for a `CREATE TRIGGER ... ON feature_flags ... audit_row_change` that is not under a `to_regprocedure` condition, with the regular expression approach of `auditwired.go:76-82`. If the flags migration is the only attach (conditional), and the audit migration file has a higher number or a later position in the pack space than the flags migration, it warns: `feature_flags can lack its audit triggers: the flags migration ran before the audit functions existed.`, `Try: run spinup db new audit_feature_flags, paste the four CREATE TRIGGER statements from docs/build/flags.md, then run spinup db migrate up`. The docs hold the statements of decision 6.1 without the `DO` block.
   - `ok`: `flags wiring is present`.
8. An app without the module renders byte-identical files. Every pack change sits under `has "flags" .modules`. `make golden` without `PACK=flags` proves it.
9. `internal/templates/packs.digest` gets the entry `flags <sha> none` (new files only). The `api` entry changes with `main.go.tmpl` and gets `none`: the lines merge three-way. The `web` entry changes with the `.tmpl` conversions and the `has "flags"` blocks, and gets `none`. The main loop sets the digest at integration. Lanes do not commit it (ADR 0025 decision 13.7).
10. `kit/MIGRATION.json` gets one more `manual` step, `flags-schema`: `Run spinup db migrate before you start the new binary when the flags module is on. Try: run spinup db migrate up.`

### 12. The OTel span event

1. `Evaluate(ctx, key)` and `BooleanEvaluation` add one span event to `trace.SpanFromContext(ctx)` when `span.IsRecording()` is true. `Resolve` adds nothing: it is pure.
2. The event name is `feature_flag.evaluation`. The attributes follow the OpenTelemetry feature flag semantic conventions (release candidate):
   | Attribute | Type | Value |
   |---|---|---|
   | `feature_flag.key` | string | the flag key |
   | `feature_flag.provider.name` | string | `spinup` |
   | `feature_flag.result.value` | bool | `Resolution.Value` |
   | `feature_flag.result.variant` | string | `on` or `off` |
   | `feature_flag.result.reason` | string | `Resolution.Reason` |
3. The event has no `feature_flag.context.id` and no user ID. A trace must not carry an account ID.
4. The code uses only `go.opentelemetry.io/otel/trace` and `go.opentelemetry.io/otel/attribute`, which the kit already requires (`kit/go.mod:22`, `:31`). The request span comes from the `otelhttp` handler of `kit/app` (`kit/app/telemetry.go:17-29`). Without telemetry the span is a no-op and `IsRecording` is false, so `Enabled` makes no allocation.
5. There is no metric in this version. The span event and the `otelhttp` metrics are enough.

### 13. Errors

1. Every error says what failed and why, and ends with a `Try:` line. This includes the sentinel errors of decision 6.5, the problem details of decision 8.5, the command errors of decision 9.4, the `Start` error of decision 6.7, and the doctor `Try:` texts of decisions 11.6 and 11.7.
2. The kit answers 401 for no user and 403 for a wrong role on the admin routes, through `RequireRole`. Both are `application/problem+json` with `no-store` (ADR 0024 decision 10.2).
3. A startup error from `flags.New` names the rule and the key: `flags: the key "New" does not match ^[a-z][a-z0-9_]{0,47}$.\nTry: correct [flags.New] in spinup.toml and run spinup g flags.`
4. A database failure at startup is a warning and a readiness failure, not a crash (decision 6.7).

### 14. Upgrade of existing apps

1. The module is opt-in. First run `spinup upgrade`. Then run `spinup module add flags`. It makes a branch for review, as for `audit` (ADR 0025 decision 13.1).
2. The new files (the migration, the two generated files, the two helpers, the three page files) get `ActionCreate`. The scaffold files of the web pack and `main.go` merge three-way. Conflicts are reported. The next steps of decision 11.5 name each manual edit.
3. The migration lives in the pack space of ADR 0028, so it clashes with no app migration. The acceptance test of #45 (`spinup module add flags` on the todo app, which has `00003_create_todos.sql`) needs no rename.
4. The default `flags.gen.ts` names `manager` as `adminRole`. An app with a custom `[auth]` must run `spinup g flags` after `module add`. `flags-drift` warns until then. This is the roles behavior of ADR 0024 decision 3.4.
5. The kit API ships in the next kit tag. This run does not change the kit pin. Generated apps in tests use `SPINUP_KIT_REPLACE`. `kit/api/current.txt` does not change (decision 4.1). `kit/api/minor.txt` stays.
6. Deploy already migrates before it starts the new binary. A new binary on an old schema serves declared defaults and reports `/readyz` 503 until `migrate` runs (decision 6.7).

### 15. Tests

1. `internal/project`: parse and default cases for `[flags]`; each `Validate` rule with the key and the rule in the error; `FlagConst`; a golden for `RenderFlagsFile` and `RenderFlagsTSFile` with zero flags and with the example; a test that the zero-flag renders equal the pack default bytes of `flags.gen.go` and `flags.gen.ts`.
2. `internal/gen/flags`: no module error; the two planned files in `Edits`; `nothing to do`; a custom `[auth]` changes `adminRole`.
3. `internal/doctor/flagsdrift`: skip, invalid toml, drift of each file, a missing file, expiry with `Deps["now"]` before and after the date.
4. `internal/doctor/flagswired`: a wired `main.go` is `ok`; each missing call warns with its line; the wrong middleware order warns; the audit-after-flags migration case warns.
5. `kit/x/flags` (no database):
   - each precedence step; a user `off` beats a global `on`; a user `on` beats a global `off`; a role match; an anonymous subject with a percentage of 100 gets `false`
   - the bucket is stable for the same key and user; a different key gives a different assignment for some users
   - a raise from 10 to 30 only adds users (property over 10,000 IDs); the split is within 2 points of the percentage at 10,000 IDs
   - an undeclared key gives `error`; a declared key with no rule gives the default with `default`; an empty snapshot gives the default
   - `Evaluate` without a scope gives `error` and does not panic
   - one request keeps its snapshot when `Replace` runs during the request
   - the span event with `tracetest`: the five attributes, no event when the span is not recording
   - `flagstest.Override` restores at cleanup; `flagstest.Context` drives a handler
   - a benchmark shows `Enabled` with 0 allocations per operation, with and without an active override table
6. `kit/x/flags` with `DATABASE_URL` (port 55433):
   - the migration runs up and down through goose on a schema with auth 00002 to 00007, with and without the audit migration; with the audit migration first, each write of both tables writes one event, and the composite `record_id` is `key:userID`
   - `Sync` inserts missing rows with the default, keeps an existing row, and reports stale keys sorted
   - `Load` reads both tables; `Refresh` swaps the snapshot
   - each `Store` write and the refresh after it; `SetRule` replaces all three fields
   - `ErrUnknownKey` for a stale row; `ErrUnknownRole`; `ErrPercentage`; `ErrUserNotFound`; `ClearUser` of a missing rule succeeds
   - a refresh failure (a dropped table) keeps the old snapshot and logs; `Ready` returns the error; a later success clears it
   - `Start` on a database without the tables does not fail, serves defaults, and `Ready` fails
   - `Prune` deletes stale rows and their user rules, and never a declared row
   - the actor reaches the trigger: the operator actor through `Command`, the user actor through `authsink.ActorContext`
   - without the audit schema, a write succeeds and `set_config` has no effect
   - the routes with `httptest`: public filtering, a signed-out visitor gets global values and 200, 401 and 403 on the admin routes through a real `auth.Auth` and `authtest.AsRole`, each write and each 422 and 404, `no-store` on every answer, 415 and 413 and 400 from the decoders, a cross-origin `PUT` gets 403 from the auth middleware
   - the command: `list` output, each action, each error with its `Try:` line, `prune` without `--yes`
7. Goldens: `make golden PACK=flags` for an app with the module; `make golden` for every other pack, which must not change.
8. Script tests (`testdata/script/flags_*.txtar`): `spinup g flags` with drift and `doctor`; a bad declaration with its `Try:` line; `module add flags` on an app with app migrations and no clash; the no-TTY error of `flags prune`. `flags_module.txtar` (F4) makes an app with `--modules flags`, runs `spinup g flags`, builds, and runs `flags list`.
9. The app gate (`testdata/script/app_gate_flags.txtar`): make an app with `--modules flags`, declare a public flag, migrate, start the API, run `flags set <key> --percent 100`, read `GET /api/flags` for a session, turn the flag off through `PUT /api/admin/flags/{key}`, and see `GET /api/flags` change within the refresh time (`FLAGS_REFRESH=1s` in the gate).
10. Web: a vitest for `flag(data, key)` (missing key, false, true) and for `listFlags` error mapping. `pnpm check` on the generated app in the gate proves the `FlagKey` union and the page types.

### 16. Docs

1. `docs/build/flags.md` (`parent: Build your app`), with runnable blocks in this order: declare a flag; `spinup g flags`; `flags.Enabled(ctx, appflags.NewCheckout)` in Go; `flag(data, 'new_checkout')` in a page; change a flag with the command, on a server through `spinup server ssh`, and on `/admin/flags`; the precedence table and the bucket; `flagstest.Override` and `flagstest.Context`; retire a flag (`expires`, `doctor`, remove the table, `g flags`, `flags prune`); limits (the refresh interval, no anonymous rollout, `--frontend-mode node` only, the undeclared role risk, the audit-after-flags attach statements). Blocks that need Postgres use `requires-env`.
2. `docs/reference/app-commands.md` gets the `flags` section. `docs/reference/configuration.md` gets `[flags.<key>]` and `FLAGS_REFRESH`. `docs/reference/doctor-checks.md` gets `flags-drift` and `flags-wired`. `docs/commands/generate-flags.md` is new. `docs/commands/module.md` lists the module. `docs/build/index.md` or `docs/index.md` links the guide.
3. The docs site navigation (`docs/site/`) is main loop only (`TestDocsSiteIsCurrent`, `internal/cli/docs_test.go:38`).
4. The docs state: the SSR and browser agreement, the 10-second window between replicas, the fact that a stale row keeps its stored value until `prune`, and the fact that a flag off answer for an undeclared key hides a typo only at compile time through the constant.

### 17. Out of scope

- Percentage of time, string or number flags, multivariate flags, and variants beyond `on` and `off`.
- Segments beyond roles, scheduled rollouts, and a kill switch with an expiry at run time.
- Bucketing of signed-out visitors (a cookie or an IP bucket).
- An OpenFeature adapter module and the `openfeature/go-sdk` dependency. The `Provider` shape makes it possible later.
- `LISTEN/NOTIFY` or any push refresh. The poll is enough at 10 seconds.
- A `g resource --flag` route gate, and a `spinup flag --env` wrapper command on the CLI.
- An OpenAPI fragment for the kit routes, and typed client calls through `openapi-fetch` for them.
- A prune route, a create route, and flag declarations at run time. `spinup.toml` is the source.
- `--reason` on the command, and an explicit `audit.Record` event.
- The `owner` field at run time, and a reminder mail at expiry.
- A metric for evaluations. The span event is enough.
- A date field on `doctor.Context`.
- Role filtering of the sidebar navigation.
- Flags for sidecar services (ADR 0022). A service calls `GET /api/flags` through the session check later.

## Consequences

- A generated app turns a feature on or off without a deploy. The change reaches every process within 10 seconds, and the process that made it at once.
- Code reads flags through generated constants. A removed flag fails the build, so no dead flag survives by accident.
- Each request costs one atomic load and one context value. An evaluation costs a map lookup and, for a percentage, one FNV-1a hash. No request runs SQL for a flag.
- The kit API is in `kit/x`, so it can change before v1 without a compatibility step.
- An app with the module must run one migration, declare its flags, run `spinup g flags`, and wire `main.go` and three web files. The doctor checks find a missed step.
- With the audit module, every flag change has a trail with the actor. Without it, only `updated_at` remains.
- A stale role in a flag rule still matches a user with that stored role. The list views mark it, and the docs tell the owner to clean it before a role rename.
- The owner must keep `[auth]` and the generated `adminRole` in step with `spinup g flags`, as for roles.

## Appendix A: Units F0 to F7

The dependency chain is F0 → (F1 ∥ F2) → F3 → F5 → F4 → F6 → F7. This changes the order of issues #50 and #51: F5 depends on F3 only, and F4 depends on F1, F3, and F5, because `main.go.tmpl` wires `Routes` and the app of `flags_module.txtar` must build. F1 and F2 share no file.

### F0 (main loop, #46): ADR 0029 and the pre-seed

Shared or main-loop files:
- `docs/adr/0029-feature-flags.md` (this document), `CLAUDE.md` (the ADR pointer line; `internal/project/project.go` is in the shared list already), `docs/design/plan.md`.
- `internal/project/project.go`: `Config.Flags`, `"flags"` in the known keys (`:170`), `FlagDecls().Validate()` in `Validate` (`:232`).
- `internal/project/flags.go` (first version only; F1 owns the file after the pre-seed, owner decision 2026-11-06): the types of decision 2.5 with stubs of `Validate`, `RenderFlagsFile`, and `RenderFlagsTSFile` that return the zero-flag bytes.
- `cmd/spinup/wire_gen.go`: blank imports of `internal/gen/flags`, `internal/doctor/flagsdrift`, and `internal/doctor/flagswired`.
- Stubs: `kit/x/flags/doc.go`, `kit/x/flags/flagstest/doc.go`, `internal/gen/flags/doc.go`, `internal/doctor/flagsdrift/doc.go`, `internal/doctor/flagswired/doc.go`, `internal/modules/flags.go` (stub `FlagsNextSteps`).
- The pack `internal/templates/packs/flags/pack.toml` with `requires = ["auth"]`, and the two default generated files with the exact bytes of decisions 3.2 and 3.3.
- The web pack conversions to `.tmpl` with unchanged bytes and the matching `source` lines in `pack.toml` and `pack.d/shell.toml`: `hooks.server.ts`, `app.d.ts`, `routes/+layout.server.ts`, `lib/components/app/user-menu.svelte`. `routes/+layout.svelte.tmpl` is a template already.
- `internal/templates/packs.digest` after each pack change (`SPINUP_GOLDEN_UPDATE=1 go test ./internal/templates -run PacksDigest`, then `none`).
- Not touched: `go.mod`, `kit/go.mod`, `kit/api/current.txt`, `internal/stackversions/stackversions.yaml`.

Check: `make build lint`, `go test ./internal/project/...`, `make golden` unchanged.

### F1 (#47): declarations, `spinup g flags`, `flags-drift`

Files: `internal/project/flags.go` (replace the stubs; F1 owns the file, it is not a shared file), `internal/project/flags_test.go`, `internal/gen/flags/**`, `internal/doctor/flagsdrift/**`, `docs/commands/generate-flags.md`.
Correction to #47: `RenderFlagsTSFile` and `web/src/lib/flags.gen.ts` belong to F1 from the start, not through a later main-loop edit. The expiry date comes from `Deps["now"]` (decision 11.6), not from a new `doctor.Context` field.
Check: `go test -race ./internal/project/... ./internal/gen/flags/... ./internal/doctor/flagsdrift/...`, `make build lint`.

### F2 (#48): the evaluator

Files: `kit/x/flags/doc.go`, `flags.go`, `evaluate.go`, `provider.go`, `context.go`, `otel.go`, `override.go`, their tests and benchmarks, `kit/x/flags/flagstest/**`.
Not F2: `store.go`, `store_test.go`, `middleware.go`, `routes.go`, `routes_test.go`, `command.go`, `command_test.go`.
F2 exports `Flags.Context` and `NewSnapshot`, which F3 needs.
Check: `go test -race ./kit/x/flags/...`, the benchmark with 0 allocs, `make build lint`.

### F3 (#49): store, sync, refresh, middleware, migration

Files: `kit/x/flags/store.go`, `store_test.go`, `middleware.go`, `middleware_test.go`, the flags migration in the pack space of ADR 0028 under `internal/templates/packs/flags/files/`, and its `[[files]]` entry in `pack.toml`.
Depends on F2 and on the implementation of ADR 0028.
Correction to #49: `Start` does not fail on a database error (decision 6.7). The audit interaction needs no option (decision 7.1).
Check: `go test -race ./kit/x/flags/...` with `DATABASE_URL`, `make build lint`.

### F5 (#51): routes

Files: `kit/x/flags/routes.go`, `routes_test.go`.
Depends on F3. Not on F4.
Correction to #51: no OpenAPI fragment (decision 8.1). The route line in `main.go.tmpl` is F4.
Check: `go test -race ./kit/x/flags/...`, `make build lint`.

### F4 (#50): module, wiring, command, `flags-wired`

Files: `internal/templates/packs/flags/pack.toml` (the api entries; F6 adds the web entries later), the `has "flags" .modules` blocks of `internal/templates/packs/api/files/api/cmd/api/main.go.tmpl`, `kit/x/flags/command.go`, `command_test.go`, `internal/modules/flags.go`, `internal/upgrade/module.go` (the `FlagsNextSteps` print), `internal/doctor/flagswired/**`, `kit/MIGRATION.json`, `testdata/script/flags_module.txtar`.
Depends on F1, F3, F5.
Correction to #50: `packs.digest` is main loop only. The command is `flagStore.Command()` (decision 9.1). `kit/MIGRATION.json` and `internal/upgrade/module.go` are added to the file set.
Check: `go test -race ./kit/x/flags/... ./internal/modules/... ./internal/doctor/flagswired/...`, `make golden PACK=flags`, `make golden`, `make script RUN='module_add*'`, `make script RUN=flags_module`, `make build lint`.

### F6 (#52): web

Files: `internal/templates/packs/flags/files/web/**` and their `[[files]]` entries in the flags `pack.toml`; the `has "flags" .modules` blocks in the web pack templates `hooks.server.ts.tmpl`, `app.d.ts.tmpl`, `routes/+layout.server.ts.tmpl`, `routes/+layout.svelte.tmpl`, and `lib/components/app/user-menu.svelte.tmpl`.
Depends on F4 and F5.
Correction to #52: the link is in the user menu (decision 10.7), the admin page gets the declared roles from the API (decision 8.4), and there is no embed case (decision 10.8). The server helper `web/src/lib/server/flags.ts` replaces the typed client.
Check: `make golden PACK=flags`, `make golden PACK=web`, `make golden PACK=auth`, `make build lint`. `pnpm check` runs in the F7 gate.

### F7 (#53): docs, scripts, gate

Files: `docs/build/flags.md`, `docs/reference/app-commands.md`, `docs/reference/configuration.md`, `docs/reference/doctor-checks.md`, `docs/commands/module.md`, `docs/build/index.md` or `docs/index.md`, `testdata/script/flags_*.txtar`, `testdata/script/app_gate_flags.txtar`.
Correction to #53: the guide is `docs/build/flags.md` with `parent: Build your app`, not `docs/flags.md`. The site navigation is main loop only.
Check: `make doctest`, `make script RUN='flags_*'`, `SPINUP_APP_GATE=1 make script RUN=app_gate_flags`, `make build lint`.

### Shared files that only the main loop edits, and who needs them

| File | Needed by |
|---|---|
| `internal/project/project.go` | F0 (fields and known keys); read by F1 |
| `internal/project/flags.go` | F0 writes the first version only. F1 owns it after that (owner decision). |
| `cmd/spinup/wire_gen.go` | F0 (three blank imports) |
| `CLAUDE.md`, `docs/design/plan.md`, `docs/adr/0029-feature-flags.md` | F0 |
| `internal/templates/packs.digest` | after F3, F4, F6 |
| web pack `.tmpl` conversions and their `source` lines | F0, so that F6 edits templates |
| `docs/site/**` | after F7 |
| `go.mod`, `kit/go.mod`, `plugin-sdk/go.mod` | nobody: no new dependency |
| `kit/api/current.txt` | nobody: `kit/x` is excluded |
| `internal/stackversions/stackversions.yaml` | nobody |

## Appendix B: Corrections to the issue sketches

| Issue | Sketch | Decision | Reason |
|---|---|---|---|
| #46 | the kit API `Enabled(ctx, key)`, `Evaluate(ctx, key)`, `Provider` | kept; plus `Resolve`, `Flags.Context`, `SubjectFrom` | jobs and commands have no request |
| #47 | `flags.gen.ts` added later through the main loop | F1 renders both files from the start | one generator, one drift check |
| #47 | the date from `doctor.Context` | `Deps["now"]`, else `time.Now()` | `doctor.Context` has no date field (`internal/doctor/registry.go:58-63`) |
| #48 | `Evaluate(snapshot, key, subject)` | `Resolve(snap, key, s)` | `Evaluate(ctx, key)` is the context form |
| #49 | `Sync(ctx, pool, Declared)`, `Load(ctx, pool)` | methods of `*Store` | one owner of the pool, the roles, and the refresh |
| #49 | the store "binds the actor through `audit.InTx` when the audit module is on" | always `audit.InTx` | `set_config` is harmless without the audit schema |
| #50 | `main.go` "fails to start on an error" | `Start` warns and `Ready` fails | `migrate` runs through the same `main.go` (`main.go.tmpl:122`, `:131`) |
| #50 | `flags.Command(pool, appflags.Set, roles.Set)` | `flagStore.Command()` | the store already holds all three |
| #50 | `packs.digest` in the F4 file set | main loop | ADR 0025 decision 13.7 |
| #50 | a `[flags]` example file in the pack | the next steps print it | `spinup.toml` is app-owned |
| #51 | an OpenAPI fragment | none | `strict-server: true` needs an implementation (`internal/gen/resource/shared.go:47`) |
| #51 | F5 after F4 | F5 after F3, F4 after F5 | `main.go.tmpl` wires `Routes` |
| #52 | the page in navigation for the admin role | a user menu link; `nav.gen.ts` unchanged | `PlanNav` has no roles and skips nested routes (`nav.go:110-118`) |
| #52 | embed mode does nothing on the web side | no embed case | `auth` is refused in embed mode (`newapp.go:150`, `select.go:66-68`) |
| #53 | `docs/flags.md` | `docs/build/flags.md` | the build guides live in `docs/build/` |
