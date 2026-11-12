---
title: ci — generate and check CI files
parent: Command guides
---

# ci — generate and check CI files

`spinup ci` writes the GitHub Actions files of your app, checks them for unsafe settings, and lists your sidecar services for a test matrix.
Use `generate` to refresh the workflow files and `verify` before you push them.

```console
$ spinup new todo --api-only --modules ci --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup ci generate
...
Created 0, updated 0 files
$ spinup ci verify --json
...
```

{: .note }
This page is the command guide. For what the generated workflows do and the secrets they need, read [Generated CI](../ci.md).

## Generate the workflow files

`generate` renders the `ci` module again from the template version that your app locks in `spinup.lock`.
It updates the managed files in `.github/` and their entries in the lock.
A managed file is a file that spinup writes and keeps up to date.
The app needs the `ci` module. If it has none, run `spinup module add ci`.

```console
$ spinup ci generate --dry-run
...
```

`--dry-run` lists the file actions and writes nothing.
`--diff` shows each change as a unified diff and writes nothing.
Pick one of the two.

```console
$ spinup ci generate --diff
```

If you edited a managed file, `generate` stops before it writes anything.
Save your edits elsewhere, then add `--force` to replace the file with the generated one.

<!-- doctest: skip -->
```console
$ spinup ci generate --force
```

The command keeps your other modules and the locked template version.
`--offline` requires local templates and makes no download.
`--json` prints a structured report.

## Check the workflow files

`verify` reads the YAML under `.github/` and reports a problem as one line each.
It requires:

- a full 40-character commit SHA for every external action (local actions in `./` are exempt)
- `permissions` at the top level of each workflow
- no head checkout in a `pull_request_target` workflow

It also runs `actionlint` when the program is on your `PATH`. Otherwise it prints a note that the check did not run.
The command exits with code 1 when it finds a problem.

<!-- doctest: skip -->
```console
$ spinup ci verify
.github/workflows/bad.yml:1: workflow requires top-level permissions
.github/workflows/bad.yml:6: uses must be pinned by a 40-character SHA (local ./ actions are exempt)
Error: CI verification failed
  2 finding(s)
  Try: correct the findings, then run spinup ci verify
[exit 1]
```

Add `--json` for a report with a `findings` array and a `notes` array.
Each finding has `path`, `line`, and `message`.

```console
$ spinup ci verify --json
...
```

## List services for a test matrix

A GitHub Actions matrix runs one job for each item.
`ci services --json` prints your declared sidecar services as one compact JSON line that a matrix can read.
Without `--json`, it prints a table.

Add `--changed-since <ref>` to keep only the services that changed.
A service stays when a file in `services/<name>/` changed in `git diff --name-only <ref>...HEAD`.
All services stay when the diff touches `spinup.toml`, `spinup.lock`, a file under `.github/`, `go.mod`, `go.work`, `api/go.mod`, or a compose file.
If `git diff` fails, the command writes a warning to stderr and keeps all services.
The generated `ci.yml` uses this flag on pull requests.

```console
$ spinup g service ocr --lang rust
...
$ spinup ci services --json
{"include":[{"name":"ocr","lang":"rust"}]}
$ spinup ci services
NAME	LANG
ocr	rust
```

## Test one service

`ci test-service <name>` runs the tests of one service in Docker.
Add `--dry-run` to print the temporary Dockerfile and skip Docker. The first line pins the base image by digest, so the example hides it.

```console
$ spinup ci test-service ocr --dry-run
...
WORKDIR /app
COPY . .
RUN cargo test --locked
```

Without `--dry-run`, the command builds that file and runs it. If a service has no tests, it says so and succeeds.

## Reference

| Flag | Used with | Meaning | Example |
| --- | --- | --- | --- |
| `--dry-run` | `generate`, `test-service` | Show what would happen. Change nothing. | `spinup ci generate --dry-run` |
| `--diff` | `generate` | Show unified diffs. Change nothing. | `spinup ci generate --diff` |
| `--force` | `generate` | Overwrite files with your edits. | `spinup ci generate --force` |
| `--json` | `generate`, `verify`, `services` | Print JSON. | `spinup ci verify --json` |
| `--changed-since` | `services` | Keep only the services that changed since a git ref. | `spinup ci services --json --changed-since origin/main` |
| `--offline` | `generate` | Use local templates only. | `spinup ci generate --offline` |

## See also

- [Generated CI](../ci.md): what each workflow does
- [Sidecar services](../services.md): declare a service
- [upgrade](upgrade.md): `spinup upgrade` also updates the CI files
- Flag reference: [ci](../site/commands/ci.md), [generate](../site/commands/ci-generate.md), [verify](../site/commands/ci-verify.md), [services](../site/commands/ci-services.md), [test-service](../site/commands/ci-test-service.md)
