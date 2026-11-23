---
title: Agent guidance
parent: Build your app
nav_order: 22
---

# Agent guidance

Each new app has files that tell an AI coding agent how to work in it.
The agent learns the generators, the file classes, the tool output, and the checks to run before it reports a task as done.

| Path | Class | Owner | What it holds |
| --- | --- | --- | --- |
| `AGENTS.md` | managed | spinup | The rules, the file classes, the tool output, a table of tasks and commands, and a finish checklist. |
| `AGENTS.project.md` | seed | You | The rules of your app. spinup writes it once and never changes it. |
| `CLAUDE.md` | seed | You | Two import lines, `@AGENTS.md` and `@AGENTS.project.md`. Claude Code reads this file. |
| `.claude/skills/spinup/SKILL.md` | managed | spinup | A short skill that points the agent at `AGENTS.md`. |

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ ls
AGENTS.md
AGENTS.project.md
CLAUDE.md
README.md
api
console
deploy
renovate.json
spinup.lock
spinup.toml
$ cat CLAUDE.md
@AGENTS.md
@AGENTS.project.md
```

Most agents read `AGENTS.md` by convention. Claude Code reads `CLAUDE.md` and follows its import lines.
Write the rules of your app in `AGENTS.project.md`. Do not edit `AGENTS.md`, because `spinup upgrade` rewrites it and [`spinup check`](../commands/check.md) reports an edit.

## The decision table

The section "Decision table" of `AGENTS.md` has one row for each common task.
A row has three cells: the task, the command to run, and a link to the page to read.

```console
$ grep '^| Add a table with a CRUD API' AGENTS.md
| Add a table with a CRUD API | `spinup g resource <name> <field:type>...` | [generate resource](https://raw.githubusercontent.com/stopyellingatme/spinup/main/docs/commands/generate-resource.md) |
```

Each pack of the app adds its own rows. An app with the `auth` module gets the rows for roles. An app with no web app has no rows for pages.
The links point to the raw Markdown of this documentation, because an agent reads plain text more easily than HTML.
The links follow the branch `main`, so an old app reads the current docs.

## Make an app with no agent files

Pass `--agent-guidance=false` to `spinup new`. The app then has none of the four files.

```console
$ cd ..
$ spinup new plain --api-only --module example.com/plain --agent-guidance=false --no-git --non-interactive
...
$ ls plain
README.md
api
console
deploy
renovate.json
spinup.lock
spinup.toml
$ grep agent_guidance plain/spinup.lock
    "agent_guidance": false,
```

`spinup.lock` keeps the answer. A later `spinup upgrade` does not add the files.
`spinup module add` does not take the flag.

## The files after an upgrade

An app that is older than the agent files gets them on its next [upgrade](../upgrading.md).
An app with no answer in `spinup.lock` gets the default, which is yes.

| File | No file in the app | Your own file in the app |
| --- | --- | --- |
| `AGENTS.md` | The upgrade creates it. | The upgrade moves your text to `AGENTS.project.md`, then creates the new `AGENTS.md`. |
| `AGENTS.project.md` | The upgrade creates it from the template. | The upgrade keeps it. Your old `AGENTS.md` text goes at its end. |
| `CLAUDE.md` | The upgrade creates it. | The upgrade keeps it and adds the two import lines when they are missing. |
| `.claude/skills/spinup/SKILL.md` | The upgrade creates it. | The upgrade stops with a conflict. Resolve it by hand. |

The codemod `0005_agent_guidance` does the moves before the new files are written. The plan names each change:

<!-- doctest: skip -->
```console
$ spinup upgrade --plan
...
Migration 0005_agent_guidance: AGENTS.project.md: Move AGENTS.md to AGENTS.project.md
Migration 0005_agent_guidance: CLAUDE.md: Import AGENTS.md and AGENTS.project.md in CLAUDE.md
...
```

When the app already has `AGENTS.project.md`, the codemod adds the heading `## Notes from the former AGENTS.md` and your old text at its end.
Review the upgrade branch before you merge it. Your notes are in `AGENTS.project.md` and `CLAUDE.md`.

## Remove the files from an app

Eject the two managed files, then delete them. A deleted file that you own stays deleted after an upgrade.

```console
$ cd todo
$ spinup eject AGENTS.md
$ spinup eject .claude/skills/spinup/SKILL.md
$ rm -r AGENTS.md .claude
$ spinup check
```

Delete `AGENTS.project.md` and `CLAUDE.md` too if you do not want them. They are seed files, so an upgrade does not add them again.
Read [eject](../commands/eject.md) for more about the file classes.

## The version notice

An agent can run an old spinup in a new app. Then it can write code that the app does not expect.
spinup compares its own version with the version that wrote `spinup.lock`. When they differ too much, each command writes one line to stderr:

```text
Warning: spinup 0.1.0-alpha.700 is older than 0.1.0-alpha.830, the version that wrote spinup.lock. Try: spinup self-update
```

A CLI that is 20 or more builds newer than the lock gets a notice that says `Try: spinup upgrade --plan`.
The notice does not need a terminal, because an agent has none. It goes to stderr, so the output of `--json` stays clean.

The notice is silent with `--json`, in CI, and for the commands `new`, `upgrade`, `self-update`, `version`, `doctor`, `docs`, `completion`, and `help`.
Set `SPINUP_NO_PIN_NOTICE=1` to turn it off. [Global flags](../reference/global-flags.md) lists the variable.

The doctor check [`cli-version`](../reference/doctor-checks.md#cli-version) reports the same state in `spinup doctor`. A development build of spinup skips it:

```console
$ spinup doctor --quick 2>/dev/null | grep cli-version
skip	cli-version	development build of spinup
```

## The docs index for agents

The file `llms.txt` lists each page of these docs with a short description. An agent reads it to find the right page.
The first lines of `AGENTS.md` link to it. It is also on the site at `https://stopyellingatme.github.io/spinup/llms.txt`.

## Add rows for a new pack

This section is for people who work on spinup itself.
Each built-in pack has the file `fragments/agents-rows.md.tmpl` in its directory under `internal/templates/packs/`.
The file holds only table rows. It has no header.

```text
| Add a background job | `spinup g job <name>` | [generate job]([[ docurl "commands/generate-job.md" ]]) |
```

Follow these rules:

1. Write each row with exactly three cells: the task, the command in backticks, and the link.
2. Write the link with `docurl` and a path under `docs/`. Link to a page, not to an anchor.
3. Write placeholders in angle brackets, such as `<name>`.
4. If a row changes a server, add "Ask the owner first" to the task cell.
5. Add a row for each new generator.

Then run the tests and update the golden trees:

<!-- doctest: skip -->
```console
$ go test ./internal/templates -run 'AgentRows|GoldenAgents'
$ go test ./internal/cli -run AgentGuidanceCommandsExist
$ SPINUP_GOLDEN_UPDATE=1 make golden PACK=agents
```

`TestAgentRows` checks that each pack has the file, that each row has three cells, and that each link names a file under `docs/`.
`TestAgentGuidanceCommandsExist` checks that each `spinup` command and flag in the rows exists. It also checks that each generator has a row.

## What is not done

- Plugin packs add no rows. A plugin renders after the main render, so `AGENTS.md` cannot see its rows.
- spinup has no files for Cursor, Copilot, or Gemini. These tools read `AGENTS.md`, or they can import it.
- The app has no copy of the docs. The rows link to the docs, so the text does not go stale.

## See also

- [new](../commands/new.md)
- [Upgrade](../upgrading.md)
- [Doctor checks](../reference/doctor-checks.md)
