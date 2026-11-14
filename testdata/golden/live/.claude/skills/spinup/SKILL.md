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
5. Do not run a command that changes a server unless the owner asks for it.

## Finish

Run the finish checklist at the end of `AGENTS.md`. Report each command that you ran and its result.
