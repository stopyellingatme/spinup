# ADR 0035: Agent guidance in generated apps

Status: accepted (owner, 2026-11-14)
Date: 2026-11-14
Extends: ADR 0005 (doc tests), ADR 0006 (upgrade classes and codemods), ADR 0007 (generators), ADR 0036 (doctor checks and docs lint)

## Context

On 2026-11-13 we compared spinup with corten, a Rust web framework of a co-worker.
Corten writes `AGENTS.md` and a skill into each new app. It also warns when the CLI version and the pin of the app differ.
A coding agent that opens a spinup app has no such help. It guesses the commands, edits managed files, and edits tool output.

A study of the code on 2026-11-14 found these facts:

- `internal/render/render.go` builds one function map for each `Render` call. A template cannot read data that a pack contributes. The word "fragment" in `internal/templates/loader.go` means a `pack.d/*.toml` file. It has no link to this ADR.
- `Pack.FS` is available to `Render`, so `Render` can read a file of each pack.
- The pack order in `Input.Packs` differs between `spinup new` (`orderPacks`), `module add`, and `upgrade`. A table that follows this order would change with the command that wrote it.
- `internal/upgrade/plan.go` (lines 174 to 216) decides each file that is not in the lock but exists on disk. A managed or scaffold file with other content gets `ActionConflict` with the reason `path exists`. A seed file gets `ActionKeep`. A seed file that is not on disk gets `ActionCreate`.
- A new managed `AGENTS.md` would therefore stop the upgrade of an app that has its own `AGENTS.md`. The plan of the milestone assumed that a class alone keeps the notes of the app. The code shows that it does not.
- `internal/migrate` runs a `Before` codemod before the old and new renders. It may move files. It runs when `From < Since <= To` and the ID is not in `lock.AppliedMigrations`.
- `lock.CLIVersion` is written by `spinup new` (`newapp.go:225`) and by each upgrade step (`run.go:364`). A development build writes `dev`. A release writes a version such as `0.1.0-alpha.830`. `compat.ParseVersion("dev")` fails.
- `printUpdateNotice` (`internal/cli/selfupdate.go`) is silent without a TTY, with `--json`, with `--offline`, in CI, and with `SPINUP_NO_UPDATE_CHECK=1`. It writes to stderr.
- The CLI has 20 generators (`spinup g --help`). The `doctor` command has the checks `openapi-drift`, `sqlc-drift`, `client-drift`, `web-bundle`, and `live-wired` from the last two milestones.
- The root `.gitignore` has the line `.claude/`. It also matches `internal/templates/packs/base/files/.claude/` and a golden tree. A pack file under it would never reach Git.
- Four golden trees contain the files of the base pack: `base`, `api`, `live`, and `mcp`.
- `docs/commands/new.md` and `docs/get-started/project-layout.md` run `ls` on a new app in doc tests. `docs/commands/new.md` says that a new API-only app has 43 files. It has 160 files with the web app.
- The flags of the base questions appear on `spinup new` and on `spinup module add`. `TestEveryCommandIsDocumented` requires each flag on the page of its command. `TestDocsSiteIsCurrent` requires the generated pages under `docs/site/commands/`.

The owner decided these points on 2026-11-14. They are binding:

1. A new app gets `AGENTS.md`, `AGENTS.project.md`, `CLAUDE.md`, and one skill. The question `agent_guidance` turns them off.
2. `AGENTS.md` and the skill are managed. `AGENTS.project.md` and `CLAUDE.md` are seed files.
3. Each pack adds its own rows to the decision table. No central list names the packs.
4. `spinup upgrade` of an app that has its own `AGENTS.md` or `CLAUDE.md` keeps the notes of the app.
5. `docs/llms.txt` is generated from the docs tree and a test finds it stale.
6. A notice warns when the CLI and the app differ in a way that matters, and a quick doctor check reports it.

## Decision

### 1. Files in the app

| Path | Source in the base pack | Class | When |
|---|---|---|---|
| `AGENTS.md` | `files/AGENTS.md.tmpl` | managed | `agent_guidance` |
| `AGENTS.project.md` | `files/AGENTS.project.md.tmpl` | seed | `agent_guidance` |
| `CLAUDE.md` | `files/CLAUDE.md` | seed | `agent_guidance` |
| `.claude/skills/spinup/SKILL.md` | `files/.claude/skills/spinup/SKILL.md` | managed | `agent_guidance` |

1.1. `AGENTS.md` follows the cross-tool convention. Claude Code reads `CLAUDE.md` and follows its `@path` lines. The two import lines of `CLAUDE.md` bring in `AGENTS.md` and `AGENTS.project.md`.
1.2. The app owns `AGENTS.project.md`. spinup never changes it after the first write.
1.3. No reference files exist. The rows of the table link to the docs. A second copy of the docs would go stale.
1.4. The four entries are in `internal/templates/packs/base/pack.d/agents.toml`. Each has `when = "agent_guidance"`.
1.5. `.gitignore` changes from `.claude/` to `/.claude/` (unit AG0). The line then ignores only the local agent folder at the repository root.

### 2. The question

2.1. `pack.d/agents.toml` of the base pack declares:

```toml
[[questions]]
key = "agent_guidance"
prompt = "Write AGENTS.md and an agent skill"
type = "bool"
default = true
```

2.2. The flag is `--agent-guidance`. `--agent-guidance=false` turns the files off. The flag appears on `spinup new` and on `spinup module add`, like the other base flags.
2.3. In a terminal, `spinup new` asks one more yes or no question. The default is yes.
2.4. With `false`, the render has none of the four files. A later `spinup upgrade` does not create them. The answer stays in `spinup.lock`.
2.5. An app that has no answer in its lock gets `true` on the next upgrade, because `ValidateAnswers` fills the default.
2.6. To turn the files off in an existing app, run `spinup eject AGENTS.md` and `spinup eject .claude/skills/spinup/SKILL.md`, then delete the files. A deleted scaffold file stays deleted.

### 3. Existing apps and the upgrade

3.1. The engine decides each file of the four as this table shows. "Own file" means a file that is not in the lock but exists on disk.

| File | No file on disk | Own file on disk |
|---|---|---|
| `AGENTS.md` (managed) | `create` | `conflict` (`path exists`), unless the codemod moved it |
| `AGENTS.project.md` (seed) | `create` | `keep` |
| `CLAUDE.md` (seed) | `create` | `keep` |
| `.claude/skills/spinup/SKILL.md` (managed) | `create` | `conflict` (`path exists`) |

3.2. A new codemod `0005_agent_guidance` (file `internal/migrate/0005_agent_guidance.go`) removes the conflict of `AGENTS.md` and adds the import lines to `CLAUDE.md`. Its `Stage` is `Before`. Its `Since` is `0.1.0-alpha.783`. The main loop sets `Since` to the number after the newest released version before the milestone merges.
3.3. The codemod does nothing when `Answers["agent_guidance"]` is `false`. It does nothing when `AGENTS.md` is a key of `Lock.Files`. This second rule covers an app that `spinup new` made with the files.
3.4. Otherwise it does these steps in this order. `Plan` lists each change and `Apply` makes it.

| Case | Change |
|---|---|
| `AGENTS.md` exists and `AGENTS.project.md` does not | Rename `AGENTS.md` to `AGENTS.project.md`. |
| Both files exist | Append the text `\n## Notes from the former AGENTS.md\n\n` and the content of `AGENTS.md` to `AGENTS.project.md`. Delete `AGENTS.md`. |
| `CLAUDE.md` exists and has no line `@AGENTS.md` | Append the missing lines of `@AGENTS.md` and `@AGENTS.project.md` at the end, each on its own line, after one blank line. |
| `CLAUDE.md` is missing, or `AGENTS.md` is missing | No change. The engine creates the seed files. |

3.5. `Apply` is idempotent. After the first run `AGENTS.md` is gone and the import lines exist, so a second run changes nothing.
3.6. The new managed `AGENTS.md` is then created in the same upgrade step. `AGENTS.project.md` and `CLAUDE.md` are kept. The app keeps its notes in both files.
3.7. An own `.claude/skills/spinup/SKILL.md` is rare. The engine stops with a conflict and the owner resolves it. The codemod does not touch it.
3.8. A later hand edit of `AGENTS.md` is an edit of a managed file. `spinup check` reports it. The notes of the app belong in `AGENTS.project.md`.
3.9. `packs.digest` (main loop): the entry of `base` gets `0005_agent_guidance`. The entries of the other packs get `none`, because a new row file is a new file.
3.10. Unit AG4 proves in `testdata/script/upgrade_agents.txtar` that: the plan names the codemod, the upgrade ends with exit 0 and no conflict, the text of the old `AGENTS.md` is in `AGENTS.project.md`, the old `CLAUDE.md` text is still there with the two import lines added once, the new `AGENTS.md` and `SKILL.md` exist, `spinup.lock` lists `0005_agent_guidance`, and `spinup check` is clean in the upgrade worktree. A Go test of the codemod covers each row of 3.4, the idempotence, and the two skip rules of 3.3.

### 4. Template functions

4.1. `internal/render` adds two functions.

```go
// fragments returns the rows that the packs of this render contribute under name.
fragments(name string) (string, error)

// docurl returns the URL of a docs page. The path is relative to docs/.
docurl(path string) (string, error)
```

4.2. `fragments` rules:

- `name` must match `^[a-z][a-z0-9-]*$`. Other names are an error.
- For each built-in pack of `Input.Packs`, it reads `fragments/<name>.md.tmpl` from `Pack.FS`. A built-in pack is a pack of `templates.Embedded()`. A pack without this file adds nothing and causes no error. A name that no pack has returns an empty string.
- It runs the file as a template with the data and the functions of the render, but without `fragments`. A call of `fragments` inside a fragment is an error ("function not defined").
- The order of the packs is fixed and does not depend on `Input.Packs`: `base`, `api`, `web`, then the other packs sorted by name. Rows keep the order of their file.
- It checks each non-blank line of the result. The line must be a Markdown table row with exactly three non-empty cells, written `| a | b | c |`. A cell has no `|`. Other lines are an error. The error says the pack, the line number, and ends with a `Try:` line.
- It trims the trailing space of each line, drops blank lines, joins the lines with `\n`, and adds no final newline.
- It caches the result of each name for the render.

4.3. `docurl` rules: `path` matches `^[a-z0-9][a-z0-9/_-]*\.(md|txt)$` and has no `..`. The result is `https://raw.githubusercontent.com/stopyellingatme/spinup/main/docs/` plus `path`. The constant is `render.DocsBase`. The raw Markdown is clean text for an agent. A site page is HTML.
4.4. Both functions are pure. They use no clock, no random value, and no map order. Output is the same for the same packs, answers, and template version.
4.5. Plugin packs add no rows. `fragments` reads only built-in packs, so it never reads a plugin fragment file, and a bad plugin fragment cannot stop a render. A plugin pack name never equals a built-in name, because the plugin overlay refuses such a name. `TestFragmentsIgnorePluginPacks` proves this.

### 5. Fragment files

5.1. Each built-in pack has `fragments/agents-rows.md.tmpl`. The delimiters are `[[` and `]]`.
5.2. A fragment holds only rows. It has no header and no blank line is required.
5.3. A row belongs to the pack of the module that it needs. Then the row exists only in an app that has the module. A fragment does not test `.modules`.
5.4. A fragment may use the answers of the render. The `web` pack uses `[[ .package_manager ]]`.
5.5. The Read cell is `[title]([[ docurl "<path>" ]])`. The page must exist under `docs/`. Rows link to a page and not to an anchor, because a raw Markdown URL has no anchors.
5.6. The packs of a render can differ from the packs of the app (the golden tests render a few packs). The table is correct for the packs that the render has.

### 6. AGENTS.md

6.1. The file is `internal/templates/packs/base/files/AGENTS.md.tmpl`. This is its complete text.

```text
# Agent guide for [[ .app_name ]]

spinup made this app. This file tells you how to work in it.
`AGENTS.project.md` holds the rules of this app. Read it too.
Do not edit this file. `spinup upgrade` rewrites it.
The index of the spinup docs is [[ docurl "llms.txt" ]].

## Rules

1. Use a generator first. Run `spinup g --help` to list the generators. Write code by hand only when no generator fits.
2. Do not edit a managed file. `spinup.lock` has the class of each file. To own a managed file, run `spinup eject <path>` first.
3. Do not edit tool output. Edit its source, then run the command in the section "Tool output".
4. Do not edit a migration after the app applied it. Run `spinup g migration <snake_name>` and write a new migration.
5. Pass `--json --non-interactive` to each `spinup` command that you run. Add `--dry-run` to a generator to see its files first.
6. Do not read, print, or commit a secret. Do not open `.env`, `.env.*`, `secrets/`, or `*.agekey`. Do not run `spinup secrets get` or `spinup secrets edit`.
7. Do not change a server, delete data, or replace a key unless the owner asks for it in this task. These commands need the request of the owner:
   - Servers: `spinup deploy` (except with `--plan` or `status`), `spinup deploy unlock`, `spinup deploy digital-ocean`, `spinup rollback`, `spinup releases prune`, `spinup infra apply`, `spinup infra destroy`, `spinup server bootstrap`, `spinup server ssh`, `spinup maintenance`, `spinup monitor silence`, and `spinup monitor test-alert`.
   - Backups: `spinup backup now`, `spinup backup prune`, `spinup backup restore`, `spinup backup schedule`, and `spinup backup verify`.
   - Secrets and keys: `spinup secrets push`, `spinup secrets rotate`, and `spinup secrets rekey`.
   - Databases: `spinup db migrate down`, `spinup db to`, `spinup db redo`, `spinup db reset`, and `spinup db restore`.
8. When a command fails, read its `Try:` line. It names the fix. Do not run the same command again without a change.

## File classes

| Class | Owner | What `spinup upgrade` does |
| --- | --- | --- |
| `managed` | spinup | It writes the new template. If you edited the file, it merges your edit or reports a conflict. It keeps a file that a generator wrote last. `spinup check` fails when you edit it. |
| `scaffold` | You | It merges the template changes into your file. |
| `seed` | You | It never changes the file. |

`spinup.toml` holds the settings of the app. Edit it by hand. `spinup.lock` records each file that spinup wrote. Never edit it.

## Tool output

| Output | Source | Command |
| --- | --- | --- |
| `api/openapi/bundle.gen.yaml` and `api/internal/gen/api.gen.go` | The files in `api/openapi/` | `spinup g openapi` |
| `api/internal/store/` | `api/db/queries/*.sql` and `api/db/migrations/` | `spinup db sqlc` |
| `web/src/lib/api/schema.d.ts` (web app only) | The files in `api/openapi/` | `spinup g openapi`, then `spinup g client` |
| `web/src/lib/api/client/` (heyapi client only) | The files in `api/openapi/` | `spinup g openapi`, then `spinup g client` |

`web/src/lib/api/client.ts` is not tool output. You can edit it.

A generator writes each other file with `.gen.` or `_gen.` in its name, for example `routes_gen.go` and `nav.gen.ts`. Do not run `spinup restore` on such a file. It writes the empty pack version and removes the generated code. To undo your edit of such a file, run `git checkout -- <path>`. If a generator builds the file from `spinup.toml`, you can run that generator again, for example `spinup g roles`.

## Decision table

Find the row that matches your task. Run its command. Read the page when the command is not enough. If the row says that the owner runs the command, or says "Ask the owner first", do not run the command before the owner agrees.

| You need | Run | Read |
| --- | --- | --- |
[[ fragments "agents-rows" ]]

## Finish checklist

Do each step. Do not report the task as done until each step passes.

1. Run `spinup test`.
2. [[ with index . "package_manager" ]]If you changed `web/`, run `cd web && [[ . ]] run check` and `cd web && [[ . ]] run test`.[[ else ]]Skip this step. This app has no web app.[[ end ]]
3. If you changed a file in `api/db/migrations/`, run `spinup db lint`.
4. Run `spinup check`. If it reports a managed file that you edited, undo your edit. For a file with `.gen.` or `_gen.` in its name, run `git checkout -- <path>`. For another file, run `spinup restore <path> --force`. If you are not sure, ask the owner.
5. Run `spinup doctor --json`. Fix each `fail` and each `warn` that names a file of this app. Report each other `fail` to the owner.
6. Tell the owner each command that you ran and its result.
```

6.2. The text has no clock value and no version. A new release does not change the file unless the rows change.
6.3. The step 2 of the checklist keeps the numbers fixed for an API-only app.

### 7. The other three files

7.1. `AGENTS.project.md` (`files/AGENTS.project.md.tmpl`, seed):

```text
# Rules of [[ .app_name ]]

Write the rules of this app here. spinup never changes this file.
`AGENTS.md` holds the rules of spinup. Do not copy them here.

## What this app does

Write two sentences.

## Rules for this app

- Write each rule as one short instruction.

## Commands

List the commands that `AGENTS.md` does not have.
```

7.2. `CLAUDE.md` (`files/CLAUDE.md`, seed). It has exactly two lines:

```text
@AGENTS.md
@AGENTS.project.md
```

7.3. `.claude/skills/spinup/SKILL.md` (`files/.claude/skills/spinup/SKILL.md`, managed, no template):

```text
---
name: spinup
description: Use this skill when you work in an app that has a spinup.lock file. It covers adding tables, API routes, pages, and modules, running checks, and upgrading templates.
---

# Work in a spinup app

## When to use

Use this skill when the project root has `spinup.toml` and `spinup.lock`.
Do not use it for other projects.

## Rules

1. Read `AGENTS.md` and `AGENTS.project.md` before you change a file.
2. Find the row of the decision table in `AGENTS.md` that matches the task. Run its command. For a generator, add `--dry-run` first.
3. Do not edit managed files, tool output, or applied migrations. `AGENTS.md` says how to change them.
4. Pass `--json --non-interactive` to each `spinup` command.
5. Do not run a command that changes a server, deletes data, or replaces a key unless the owner asks for it. Rule 7 of `AGENTS.md` lists these commands.

## Finish

Run the finish checklist at the end of `AGENTS.md`. Report each command that you ran and its result.
```

### 8. Rows of each pack

8.1. Each block is the complete text of `fragments/agents-rows.md.tmpl` of the pack. The unit places the text and does not change it. `<path>` and other placeholders in angle brackets stay as written.

`base` (unit AG2):

```text
| Run the app in development. It runs until you stop it. | `spinup dev` | [dev]([[ docurl "commands/dev.md" ]]) |
| Run the API tests | `spinup test` | [Test your app]([[ docurl "build/testing.md" ]]) |
| Find edits of managed files | `spinup check` | [check]([[ docurl "commands/check.md" ]]) |
| See how a file differs from its template | `spinup diff <path>` | [diff]([[ docurl "commands/diff.md" ]]) |
| Undo your edit of a managed file. For a file with `.gen.` or `_gen.` in its name, run `git checkout -- <path>`. | `spinup restore <path> --force` | [restore]([[ docurl "commands/restore.md" ]]) |
| Keep your edit of a managed file | `spinup eject <path>` | [eject]([[ docurl "commands/eject.md" ]]) |
| List the modules, or add one | `spinup module list`, then `spinup module add <name>` | [module]([[ docurl "commands/module.md" ]]) |
| Check the health of the app | `spinup doctor --json` | [Doctor checks]([[ docurl "reference/doctor-checks.md" ]]) |
| Upgrade the templates | `spinup upgrade --plan`, then `spinup upgrade` | [Upgrade]([[ docurl "upgrading.md" ]]) |
```

`api`:

```text
| Add a table with a CRUD API | `spinup g resource <name> <field:type>...` | [generate resource]([[ docurl "commands/generate-resource.md" ]]) |
| Add a table with no API | `spinup g model <name> <field:type>...` | [generate resource]([[ docurl "commands/generate-resource.md" ]]) |
| Add one plain HTTP handler | `spinup g handler <name>` | [generate resource]([[ docurl "commands/generate-resource.md" ]]) |
| Change a table with a new migration | `spinup g migration <snake_name>`, then `spinup db migrate up` | [generate resource]([[ docurl "commands/generate-resource.md" ]]) |
| Rebuild the Go server code after an OpenAPI change | `spinup g openapi` | [generate openapi]([[ docurl "commands/generate-openapi.md" ]]) |
| Rebuild the Go query code after a SQL change | `spinup db sqlc` | [db]([[ docurl "commands/db.md" ]]) |
| Add a sidecar service in Rust, Python, or Node | `spinup g service <name> --lang rust` | [generate service]([[ docurl "commands/generate-service.md" ]]) |
| List the HTTP routes | `spinup routes --json` | [routes]([[ docurl "commands/routes.md" ]]) |
| Check the migrations for unsafe changes | `spinup db lint` | [db lint]([[ docurl "commands/db-lint.md" ]]) |
| Fix the doctor checks `openapi-drift` and `sqlc-drift` | `spinup g openapi`, then `spinup db sqlc` | [Doctor checks]([[ docurl "reference/doctor-checks.md" ]]) |
```

`web`:

```text
| Add a page | `spinup g page <route>` | [generate page]([[ docurl "commands/generate-page.md" ]]) |
| Add a shadcn-svelte component. This needs the network. | `spinup g component <name>` | [generate component]([[ docurl "commands/generate-component.md" ]]) |
| Rebuild the TypeScript client after an OpenAPI change | `spinup g client` | [generate client]([[ docurl "commands/generate-client.md" ]]) |
| Check the types and run the web tests | `cd web && [[ .package_manager ]] run check`, then `cd web && [[ .package_manager ]] run test` | [Test your app]([[ docurl "build/testing.md" ]]) |
| Check the size of the web bundle | `cd web && [[ .package_manager ]] run build`, then `spinup doctor --json` | [Web bundle budget]([[ docurl "web-bundle.md" ]]) |
| Fix the doctor check `client-drift` | `spinup g client` | [Doctor checks]([[ docurl "reference/doctor-checks.md" ]]) |
```

`auth`:

```text
| Limit a resource to roles | `spinup g resource <name> <field:type>... --role <roles>` | [Roles]([[ docurl "build/roles.md" ]]) |
| Share the rows of a resource between users | `spinup g resource <name> <field:type>... --shared` | [Roles]([[ docurl "build/roles.md" ]]) |
| Limit a page to roles | `spinup g page <route> --server --role <roles>` | [generate page]([[ docurl "commands/generate-page.md" ]]) |
| Change the roles. Edit `[auth]` in `spinup.toml` first. | `spinup g roles` | [Roles]([[ docurl "build/roles.md" ]]) |
| List the accounts, or change the role of an account | `cd api && go run ./cmd/api user list`, then `cd api && go run ./cmd/api user set-role --email <email> --role <role>` | [App binary commands]([[ docurl "reference/app-commands.md" ]]) |
```

`jobs`:

```text
| Add a background job | `spinup g job <name>` | [generate job]([[ docurl "commands/generate-job.md" ]]) |
| Run the jobs worker by hand | `cd api && go run ./cmd/worker` | [Jobs]([[ docurl "build/jobs.md" ]]) |
```

`mail`:

```text
| Add an email template and its sender | `spinup g mailer <name> <field:type>...` | [generate mailer]([[ docurl "commands/generate-mailer.md" ]]) |
| Read the mail that the app sends in development | `spinup dev`, then open `http://localhost:8025` | [Mail]([[ docurl "mail.md" ]]) |
```

`audit`:

```text
| Record who changed a table | `spinup g audit <resource-or-table>` | [generate audit]([[ docurl "commands/generate-audit.md" ]]) |
| Read the audit events of a table | `cd api && go run ./cmd/api audit list --resource <table>` | [Audit trail]([[ docurl "audit.md" ]]) |
```

`flags`:

```text
| Add or change a feature flag. Edit `[flags]` in `spinup.toml` first. | `spinup g flags` | [Feature flags]([[ docurl "build/flags.md" ]]) |
| Turn a flag on while the app runs | `cd api && go run ./cmd/api flags enable <key>` | [Feature flags]([[ docurl "build/flags.md" ]]) |
| Turn a flag off while the app runs | `cd api && go run ./cmd/api flags disable <key>` | [Feature flags]([[ docurl "build/flags.md" ]]) |
```

`apikeys`:

```text
| Change the plans or quotas of API keys. Edit `[apikeys]` in `spinup.toml` first. | `spinup g apikeys` | [API keys and plans]([[ docurl "build/api-keys.md" ]]) |
| Create an API key. The owner runs this command, because it prints the secret token. Do not run it yourself. | `cd api && go run ./cmd/api apikeys create <email> --name <name>` | [API keys and plans]([[ docurl "build/api-keys.md" ]]) |
```

`mcp`:

```text
| Add a typed MCP tool | `spinup g mcp tool <name>` | [generate mcp]([[ docurl "commands/generate-mcp.md" ]]) |
| Add MCP tools for a new resource | `spinup g resource <name> <field:type>... --mcp` | [MCP server]([[ docurl "build/mcp.md" ]]) |
```

`billing`:

```text
| Change the plans or prices. Edit `[billing]` in `spinup.toml` first. | `spinup g billing` | [Billing with Stripe]([[ docurl "build/billing.md" ]]) |
| Check the Stripe setup of the app | `cd api && go run ./cmd/api billing check` | [Billing with Stripe]([[ docurl "build/billing.md" ]]) |
```

`live`:

```text
| Add live updates to a resource that exists | `spinup g live <resource>` | [generate live]([[ docurl "commands/generate-live.md" ]]) |
| Add live updates to a new resource | `spinup g resource <name> <field:type>... --live` | [Live data]([[ docurl "build/live.md" ]]) |
| List the live feeds | `cd api && go run ./cmd/api live feeds` | [Live data]([[ docurl "build/live.md" ]]) |
```

`deploy`:

```text
| See what a deploy would change | `spinup deploy --plan --env <env>` | [deploy]([[ docurl "commands/deploy.md" ]]) |
| Show the current release | `spinup deploy status --env <env>` | [deploy]([[ docurl "commands/deploy.md" ]]) |
| Ship the app. Ask the owner first. | `spinup deploy --env <env>` | [Deploy to your own server]([[ docurl "deploy.md" ]]) |
```

`monitoring`:

```text
| Add a Grafana dashboard | `spinup g dashboard <name> --panel <title=PromQL>` | [generate dashboard]([[ docurl "commands/generate-dashboard.md" ]]) |
| Add a Prometheus alert | `spinup g alert <name> --expr <PromQL>` | [generate alert]([[ docurl "commands/generate-alert.md" ]]) |
| Read the alerts and logs of a deployed app | `spinup monitor alerts --env <env>`, then `spinup monitor logs --env <env>` | [monitor]([[ docurl "commands/monitor.md" ]]) |
```

`backups`:

```text
| List the backups | `spinup backup list --env <env>` | [backup]([[ docurl "commands/backup.md" ]]) |
| Check that a backup restores. It runs on the server. Ask the owner first. | `spinup backup verify --env <env>` | [backup]([[ docurl "commands/backup.md" ]]) |
```

`secrets`:

```text
| See which secrets changed | `spinup secrets diff --env <env>` | [secrets]([[ docurl "commands/secrets.md" ]]) |
| Set a secret. Ask the owner for the value. | `spinup secrets set <NAME> --env <env>` | [Secrets]([[ docurl "deploy/secrets.md" ]]) |
```

`ci`:

```text
| Rebuild the CI workflows | `spinup ci generate` | [Generated CI]([[ docurl "ci.md" ]]) |
| Check the CI workflows | `spinup ci verify` | [Generated CI]([[ docurl "ci.md" ]]) |
```

`tofu`:

```text
| Preview the infrastructure changes | `spinup infra plan --env <env>` | [Hetzner Cloud with OpenTofu]([[ docurl "infra.md" ]]) |
| Apply the infrastructure changes. Ask the owner first. | `spinup infra apply --env <env>` | [Hetzner Cloud with OpenTofu]([[ docurl "infra.md" ]]) |
```

8.2. The generator of each of the 20 kinds has a row: `resource`, `model`, `handler`, `migration`, `openapi`, and `service` in `api`. `page`, `component`, and `client` in `web`. `roles` in `auth`. `job` in `jobs`. `mailer` in `mail`. `audit` in `audit`. `flags` in `flags`. `apikeys` in `apikeys`. `mcp` in `mcp`. `billing` in `billing`. `live` in `live`. `dashboard` and `alert` in `monitoring`.
8.3. A row that tells an agent to change a server says "Ask the owner first". This repeats rule 7. A row whose command prints a secret says that the owner runs the command. `TestAgentGuidanceOwnerCommands` checks both.

### 9. Tests

| Test | Unit | What it proves |
|---|---|---|
| `TestFragments*` in `internal/render` | AG1 | Order does not depend on `Input.Packs`. A pack with no file adds nothing. A bad name, a bad row, and a call of `fragments` inside a fragment fail with a `Try:` line. Output is deterministic. `docurl` accepts and refuses the paths of 4.3. |
| `TestGoldenBase` (extended) | AG2 | The four files exist with `agent_guidance` true and none exist with `false`. The base rows are in `AGENTS.md`. |
| `TestAgentRows` | AG3 | Each built-in pack has `fragments/agents-rows.md.tmpl`. Each row has three cells. Each Read link points to a file under `docs/`. Rows of reversed packs are equal. The rows of a pack are absent when the pack is absent. |
| `TestGoldenAgents` | AG3 | The golden tree `agents` has the four files of an app that has all 18 packs. |
| `TestAgentGuidanceCommandsExist` | AG4 | See 9.1. |
| `TestCodemodAgentGuidance` | AG4 | The rows of 3.4, the idempotence, and the skip rules of 3.3. |
| `upgrade_agents.txtar` | AG4 | The checks of 3.10. |
| `TestLLMS*` and `TestLLMSIsCurrent` | AG5 | See section 10. |
| `TestPinState`, `TestPinNotice*`, `TestCLIVersionCheck` | AG6 | See sections 11 and 12. |

9.1. `TestAgentGuidanceCommandsExist` (file `internal/cli/agents_test.go`) renders an app with all packs. It reads `AGENTS.md` and `SKILL.md`.

- It builds the command tree with `newRoot`.
- Each backtick span that starts with `spinup ` is a command. The test removes the words that start with `<` or `[` and the words `then` and `and`. It calls `root.Find` with the rest. The command must exist and must not be the root. Each `--flag` must exist on the command or on the root. The flag `--help` is always valid.
- Each of the children of `generate` appears as a span `spinup g <kind>` somewhere in `AGENTS.md`.
- Each span that ends in `-drift`, `-wired`, or `-bundle` is the ID of a registered doctor check.
- Each URL that starts with the `docurl` base names a file that exists under `docs/`.

### 10. docs/llms.txt

10.1. The file follows the llms.txt convention: an H1 title, a block quote with a summary, a short note, and H2 sections of links. Each link has a short description.
10.2. The generator is `docsite.LLMS(docs fs.FS) ([]byte, error)` in `internal/docsite/llms.go`. `docs` is the `docs/` directory.
10.3. Pages: each `*.md` file under `docs/`, except the directories `adr`, `design`, and `site`, the file `index.md` of the root, and a page with `nav_exclude: true`. The title is the front matter `title`. A page without a title is an error.
10.4. Section: the front matter `parent`. A page without `parent` goes to the section "Overview". The section "Contributing" is named "Optional". Order: "Overview", then the parents by the `nav_order` of the page that has this title and no parent, then the other parents by name, then "Optional". In a section, pages follow `nav_order` (a missing one is 1000), then the title.
10.5. Description: the first sentence of the first prose paragraph after the H1. A line that starts with a code fence, `|`, `#`, `<`, or `{:` is not prose. A code fence is three or more backticks or tildes. The block ends at a line with only the same character, at least as many times. Link syntax becomes its text. Long text is cut at a word, at 200 characters.
10.6. URL: the text `https://raw.githubusercontent.com/stopyellingatme/spinup/main/docs/` plus the path of the page. This is the value of `render.DocsBase`. `docsite` imports the constant. The two packages do not import each other in the other direction, so no cycle exists. Unit AG5 starts after AG1, so the constant exists.
10.7. Fixed text: the title `# spinup`. The summary is `> spinup creates and operates a full-stack app: a Go API, a SvelteKit web app, PostgreSQL, a deploy to a VM, and monitoring. It upgrades the app and keeps your edits.` The note is `Each link points to a Markdown page. Read "Get started" first. The section "Optional" is for people who work on spinup itself.`
10.8. `TestLLMSIsCurrent` (in `internal/cli`) compares `docs/llms.txt` with `docsite.LLMS`. With `SPINUP_GOLDEN_UPDATE=1` it writes the file. This follows `TestDocsSiteIsCurrent`. No command writes the file.
10.9. Jekyll copies `docs/llms.txt` to the site as `https://stopyellingatme.github.io/spinup/llms.txt` because the file has no front matter. `_config.yml` does not change.
10.10. A unit that adds a page, or changes the first sentence of a page, runs the update. A unit that does not add a page does not touch `docs/llms.txt`.

### 11. The pin notice

11.1. `compat.CheckPin(cli, pin string) PinAdvice` in `internal/compat/pin.go`:

```go
type PinState int

const (
	PinUnknown PinState = iota // a version is "dev", empty, or not a semantic version
	PinMatch
	PinCLIOlder
	PinCLINewer
)

// PinSlack is the gap of a pre-release counter that the CLI must pass before a newer CLI is reported.
const PinSlack = 20

type PinAdvice struct {
	State        PinState
	Message, Try string
}
```

11.2. The rule, in this order:

1. If one of the two strings does not parse with `ParseVersion`, or is a Go pseudo-version, the state is `PinUnknown`. This covers `dev`, an empty pin, and each pseudo-version form: `vX.0.0-yyyymmddhhmmss-hash`, `vX.Y.Z-pre.0.yyyymmddhhmmss-hash`, and `vX.Y.Z-0.yyyymmddhhmmss-hash`.
2. If the major or the minor differs, the state is `PinCLIOlder` when the CLI is lower, or `PinCLINewer` when it is higher.
3. If both versions have a pre-release and the same major, minor, and patch, then:
   - when the pre-release text before the last dot is equal and the last part is a number in both, the CLI is `PinCLIOlder` when its number is lower. It is `PinCLINewer` when its number is at least `PinSlack` higher. Otherwise it is `PinMatch`.
   - in any other case the state follows `Compare`: lower is `PinCLIOlder`, higher is `PinCLINewer`.
4. In all other cases the state is `PinMatch`. A patch difference of two release versions is a match.

11.3. This rule is useful before v1. Every version is `0.1.0-alpha.N`, so a rule on the minor would never fire. An older CLI always fires, because it can write code that the app does not understand. A newer CLI fires after 20 builds. Change `PinSlack` in one place to tune it. After v1 the rule on the minor and the major takes over.
11.4. Texts. The version has no `v` prefix.

| State | Message | Try |
|---|---|---|
| `PinCLIOlder` | `spinup <cli> is older than <pin>, the version that wrote spinup.lock` | `spinup self-update` |
| `PinCLINewer` | `spinup <cli> is newer than <pin>, the version that wrote spinup.lock` | `spinup upgrade --plan` |

11.5. The notice is one line on stderr: `Warning: ` plus the message, then `. Try: ` plus the try text. Example: `Warning: spinup 0.1.0-alpha.700 is older than 0.1.0-alpha.830, the version that wrote spinup.lock. Try: spinup self-update`.
11.6. `internal/cli/pin_notice.go` has `printPinNotice(c, app, errOut)`. `root.go` calls it in `PersistentPreRunE` after `printUpdateNotice`. It uses the variable `Version` and not `currentVersion()`, because `new` and `upgrade` write `Version` to the lock.
11.7. The notice is silent when:

- `--json` is set. The doctor check carries the information in JSON runs.
- The environment has `CI` or `SPINUP_NO_PIN_NOTICE=1`.
- The top-level command is `new`, `upgrade`, `self-update`, `version`, `doctor`, `docs`, `completion`, or `help`.
- `project.FindRoot` finds no project, or `spinup.lock` cannot be read, or it has no `cli_version`.
- The state is not `PinCLIOlder` or `PinCLINewer`.

11.8. The notice does not need a TTY. This differs from the update notice. An agent runs without a TTY, and it is the main reader. The line goes to stderr, so stdout and JSON stay clean.
11.9. The reader of the lock reads only `cli_version` with a small JSON struct. It does not call `manifest.Load`, so a damaged lock cannot stop a command.
11.10. The environment variable is documented in `docs/reference/global-flags.md`.

### 12. The doctor check cli-version

12.1. ID `cli-version`, title "CLI and lock version", scope `Local`, `Quick: true`. The file is `internal/doctor/checks/cliversion.go` with its own `init`. The package `checks` already has a blank import, so no wire file changes.
12.2. It reads `c.Deps["cli_version"]` and `cli_version` of `spinup.lock`, and calls `compat.CheckPin`. It never returns `Fail`.

| Case | Status | Message | Try |
|---|---|---|---|
| `c.Root` is empty | `skip` | `outside a spinup project` | none |
| CLI version is empty or `dev` | `skip` | `development build of spinup` | none |
| The lock cannot be read | `skip` | `spinup.lock cannot be read` | none |
| `PinUnknown` | `skip` | `spinup.lock records <pin>, which is not a release version` | none |
| `PinMatch` | `ok` | `CLI <cli>, spinup.lock <pin>` | none |
| `PinCLIOlder` or `PinCLINewer` | `warn` | the message of 11.4 | the try text of 11.4 |

12.3. `docs/reference/doctor-checks.md` gets a row and a section for `cli-version`.

### 13. Docs and shared files

13.1. New page `docs/build/agents.md` (unit AG4): front matter `title: Agent guidance`, `parent: Build your app`, `nav_order: 22`. It names the four files, the flag, the upgrade rules, the pin notice, `llms.txt`, and how a contributor adds rows for a new pack. It has runnable `console` blocks.
13.2. Unit AG2 edits `docs/commands/new.md` and `docs/commands/module.md` for the flag, the `ls` output, and the file counts (47 files for an API-only app, 164 with the web app). It edits `docs/get-started/project-layout.md` for the same `ls` output and the tree. It regenerates `docs/site/commands/new.md` and `docs/site/commands/module-add.md`.
13.3. Main loop only: `CLAUDE.md` of the repository (one pointer line), `docs/design/plan.md` (the N3 section), `.gitignore` (1.5), `internal/templates/packs.digest` (3.9), and `CHANGELOG.md`.
13.4. No wire file changes. No new Go module. No new npm package.

### 14. Units

See `docs/design/plan.md`, section "N3". The graph:

| Unit | Outcome | Deps |
|---|---|---|
| AG0 (main loop) | ADR, `.gitignore`, pointer line, plan section | none |
| AG1 | `fragments` and `docurl` | AG0 |
| AG2 | Base files, the question, the base rows, `G base`, docs of the flag | AG1 |
| AG3 | Rows of 17 packs, `TestAgentRows`, goldens `api`, `live`, `mcp`, `agents` | AG2 |
| AG4 | `TestAgentGuidanceCommandsExist`, the codemod, `upgrade_agents`, the guide, `llms.txt` update | AG3, AG5 |
| AG5 | `docs/llms.txt` and its tests | AG1 |
| AG6 | Pin notice and `cli-version` | AG0 |

## Changes from the review (2026-11-14)

Unit F1 made these changes after review R1. The texts in sections 6, 7.3, and 8 are the new texts.

- Finding 2: `spinup restore` writes the pack version of a file. For `routes_gen.go` and other generated files, it removes the generated code. `AGENTS.md` now sends a file with `.gen.` or `_gen.` in its name to `git checkout -- <path>` or to its generator. The restore row and checklist step 4 use `spinup restore <path> --force` only for other managed files.
- Finding 3: the `apikeys create` row says that the owner runs the command, because it prints the token.
- Finding 5: the `flags` pack has a row for `flags enable <key>` and a row for `flags disable <key>`.
- Finding 6: `fragments` reads only built-in packs (4.2, 4.5).
- Finding 8: rule 7 lists the commands that change a server, delete data, or replace a key, with `spinup secrets rotate` and `spinup secrets rekey`. Rule 6 forbids `spinup secrets get` and `spinup secrets edit`. Rule 5 of `SKILL.md` points to rule 7.
- Finding 9: the `backup verify` row says that it runs on the server, and "Ask the owner first." The decision table says that the agent waits for the owner on such a row.
- Finding 10: the table "Tool output" names `api/internal/gen/api.gen.go`, `web/src/lib/api/schema.d.ts`, and `web/src/lib/api/client/` (heyapi only). It says that `web/src/lib/api/client.ts` is not tool output.
- Finding 11: the row `managed` of the file class table says that an upgrade merges an edited file or reports a conflict, and keeps a file that a generator wrote last.
- Finding 12: `CheckPin` returns `PinUnknown` for each Go pseudo-version form (11.2).
- Finding 13: the `llms.txt` description skips tilde fences too (10.5).

## Deviations from the plan

| Plan | This ADR | Reason |
|---|---|---|
| The classes keep the notes of an app that has its own `AGENTS.md`. | A codemod `0005_agent_guidance` does it. | A managed file that exists on disk and is not in the lock is a conflict (`plan.go`). |
| `fragments` only. | `docurl` too. | One constant for the docs URL, and the test can map each link to a file. |
| The notice fires when major or minor differs. | The rule of section 11 also covers pre-release builds. | Every version is `0.1.0-alpha.N` today. |
| The notice has the conditions of the update notice. | No TTY needed. | The agent has no TTY. |
| Rows for the api, web, and module packs. | Rows for all 18 packs, and a test that each pack has a file. | The test then finds a future pack with no rows. |
| AG3 regenerates `G agents`. | AG3 also regenerates `api`, `live`, and `mcp`. | These trees contain base files. |
| AG4 has no dependency on AG5. | AG4 depends on AG5. | The new guide page must be in `llms.txt`. |
| Docs links go to the site. | Links go to the raw Markdown. | An agent reads clean text. The raw URL has no anchors, so rows link to pages. |
| `.gitignore` is not in the plan. | AG0 changes it. | The line `.claude/` hides the pack source. |
| The doctor check may need a wire file. | It lives in the package `checks`. | That package is wired. |

## Not done, with the reason

| Item | Reason |
|---|---|
| Rows from plugin packs | A plugin renders after the main render. A second pass would rewrite `AGENTS.md`. A later ADR can add it. |
| Reference files in the app (a copy of the docs) | The copy would go stale. The rows link to the docs. |
| One skill for each module | The decision table already points to the right page. More skills cost tokens at each session. |
| A `spinup agents` command | The files exist after `new`. `module add` and `upgrade` keep them. A command adds a surface with no gain. |
| A command that turns guidance off in an existing app | `eject` and `rm` do it (2.6). A second way adds a lock edit. |
| A rewrite of an own `SKILL.md` | The case is rare. The conflict is safe and visible. |
| Cursor, Copilot, and Gemini files | They read `AGENTS.md`, or they can import it. A file for each tool would repeat the text. |
| A check that `AGENTS.md` matches the commands of the app at run time | `TestAgentGuidanceCommandsExist` checks the text at build time. The app has no way to run the check. |
| Web site HTML links in the rows | A raw Markdown link is better for an agent (section 4.3). |
| A notice for a kit pin that differs | `kit-version` covers it. |
| Trusted Types and a stricter CSP | Out of scope. See ADR 0036. |

## Consequences

- A new app has four more files (47 for an API-only app). Each upgrade can change `AGENTS.md` and `SKILL.md`, and the diff is easy to read.
- A pack author must add `fragments/agents-rows.md.tmpl`. `TestAgentRows` fails without it, and the guide says how.
- A docs page that moves or goes away breaks a row. `TestAgentRows` finds it.
- The URLs follow the branch `main`. An old app reads the new docs. This matches the rest of the docs.
- A new generator without a row fails `TestAgentGuidanceCommandsExist`.
- The first upgrade of an old app moves its `AGENTS.md` to `AGENTS.project.md`. The owner sees the change in the upgrade branch and can review it before the merge.
- A stderr line can appear in the logs of a script. The owner turns it off with `SPINUP_NO_PIN_NOTICE=1`.
- Four golden trees change and one is new (`agents`). The digest of all 18 packs changes.

## Appendix A: Facts checked on 2026-11-14

| Fact | Evidence |
|---|---|
| A managed file that exists and is not in the lock is a conflict. | `internal/upgrade/plan.go:194-216` and `plan.go:381-386` (`reason == "path exists"` gives `ActionConflict`). |
| A seed file that exists is kept. A missing seed file is created. | `plan.go:186-191`. |
| A `Before` codemod may move files. | `internal/migrate/contract.go`, `Stage Before`. |
| A new app has no `AppliedMigrations`. | `internal/gen/newapp/newapp.go` has no write to the field. `From < Since` keeps the codemod from a new app. The skip rule 3.3 covers a dev lock. |
| `cli_version` is `dev` for a development build. | A new app made with the built binary has `cli_version` `dev` and `template_version` `0.0.0-dev`. |
| `--agent-guidance` appears on two commands. | `spinup module add --help` lists `--db-version` and `--module`, which are base questions. |
| The root `.gitignore` hides `.claude/` in the pack. | `git check-ignore -v` on the pack path prints `.gitignore:37:.claude/`. |
| The CLI has 20 generators. | `spinup g --help`. |
| New app file counts today. | `spinup new --api-only` makes 43 files. `spinup new` makes 160 files. |
| The order of packs differs by command. | `orderPacks` in `newapp.go` sorts by name with dependencies first. `module add` (`select.go`) uses its own loop. |

## Appendix B: Units

The unit files are in the run directory.

- AG0 (main loop): the ADR commit, the `.gitignore` line, the pointer line in `CLAUDE.md`, the N3 section of `docs/design/plan.md`.
- AG1: `fragments` and `docurl`. After AG0.
- AG2: base pack files, the question, the base rows, the base golden, the docs of the flag. After AG1.
- AG3: the rows of 17 packs, `TestAgentRows`, `TestGoldenAgents`, and four golden trees. After AG2.
- AG4: the command test, the codemod, the upgrade script, the guide, and the `llms.txt` update. After AG3 and AG5.
- AG5: `docs/llms.txt` and its tests. After AG1.
- AG6: the pin notice and the doctor check. After AG0.

Dispatch order for two lanes: AG1 with AG6, then AG2 with AG5, then AG3, then AG4. The main loop then runs the gate, sets the digests (3.9), and sets `Since` (3.2).
