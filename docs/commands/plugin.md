---
title: plugin — add and manage plugins
parent: Command guides
---

# plugin — add and manage plugins

A plugin adds generators and template files to your app.
`spinup plugin` adds, inspects, verifies, updates, and removes plugins in an existing app.
Spinup shows you what a plugin contains and asks you to trust it before it records anything.

```console
$ spinup new demo --api-only --module example.com/demo --modules '' --no-git --non-interactive
...
$ cd demo
$ spinup plugin new paper --out plugins/paper
...
$ spinup plugin add path:./plugins/paper --trust
Name: paper
Version: 0.1.0
...
Next: spinup upgrade --plan
```

There are two kinds of plugin:

| Kind | What it holds | Does spinup run its code? |
| --- | --- | --- |
| Pack plugin | Templates, metadata, and declarative generators | No |
| Executable plugin | A pack plus a program for generators and checks | Yes, with your full user privileges |

This page covers pack plugins.
For an executable plugin, add `--trust-exec` as described below and read [Executable plugins](../plugins-exec.md).
To write a plugin, see [plugin new](plugin-new.md).

## Add a plugin

`spinup plugin add <source>` shows the plugin and records your trust.
The source is `path:<directory>` for a local plugin, or a Git URL for `https`, `ssh`, or `git`.
A Git source needs a fixed tag with `--version`.
The command rejects local Git transport, so use `path:` for a local directory.

<!-- doctest: skip -->
```console
$ spinup plugin add git:https://github.com/example/spinup-paper --version v1.0.0 --trust
```

Before it records anything, the command prints the name, version, source, hash, files, and generators.
Read them, then approve.
In a terminal, the command asks you to type `yes`.
Without a terminal, pass `--trust`.
The global `--yes` does not grant trust.

The command writes the declaration to `spinup.toml`, the hash to `spinup.lock`, and an approval to `.spinup/plugins.allow`.
It keeps your other settings and comments.
It does not render the files.
Run `spinup upgrade --plan`, then `spinup upgrade`, to get the generated files.

## What you approve

A trust record ties a plugin name to its source and a SHA-256 hash of its files.
If the source changes, the hash changes, and `spinup plugin verify` fails until you approve the new version.
That way a plugin cannot change after you read it.
[Plugin trust](../policy/plugins.md) explains the model.

## List and inspect plugins

`spinup plugin list` shows each plugin with its version, source, and locked hash.
`spinup plugin info <name>` shows the verified metadata, the generators, and the file paths.

```console
$ spinup plugin list
...
$ spinup plugin info paper
Name: paper
...
Generator: paper-note Create a note
...
```

Add `--json` to `plugin list` for scripts.
In an app with no plugins, it prints `[]`.

## Verify in CI

`spinup plugin verify` fetches every source again and checks its hash against the trust records.
It exits with code 1 if a source differs.
Run it in CI to catch a plugin that changed.

```console
$ spinup plugin verify
...
```

## Update a plugin

`spinup plugin update <name>` approves a new version.
The command prints the old and new hash.
For a local plugin that you edited, omit `--version`:

```console
$ spinup plugin update paper --trust
...
Next: spinup upgrade --plan
```

For a Git plugin, pass the new tag:

<!-- doctest: skip -->
```console
$ spinup plugin update paper --version v2.0.0 --trust
```

Run `spinup upgrade --plan` to review the files the new version changes.

## Remove a plugin

`spinup plugin remove <name>` removes the declaration, the lock entry, and the approval.
Files that the plugin already rendered stay in your app.
The next upgrade deletes the unmodified ones and keeps the ones you edited.

```console
$ spinup plugin remove paper
...
```

## Trust an executable plugin

An executable plugin runs a program with your user privileges.
Read the source and the binaries first.
Then trust it by name with `--trust-exec`:

<!-- doctest: skip -->
```console
$ spinup plugin add path:../greetings --trust-exec greetings
```

## Preview and offline use

`--dry-run` shows the change and writes no trust record and no cache.
`--offline` uses only a plugin you approved before.
The cache lives under your user cache directory, in `spinup/plugins/<name>/<version>/`.
If the entry is missing offline, the command fails.

```console
$ spinup plugin add path:./plugins/paper --trust --dry-run
...
```

## Flags

| Flag | Commands | What it does | Example |
| --- | --- | --- | --- |
| `--trust` | `add`, `update` | Approve the displayed source and hash without a prompt. | `spinup plugin add path:./p --trust` |
| `--trust-exec <name>` | `add`, `update` | Approve running the named executable plugin. | `--trust-exec greetings` |
| `--version <tag>` | `add`, `update` | The fixed Git tag or path version. | `--version v1.0.0` |
| `--dry-run` | all | Show the change and write nothing. | `spinup plugin update paper --dry-run` |
| `--offline` | all | Use only cached, approved plugins. | `spinup plugin verify --offline` |
| `--json` | `list` | Print JSON. | `spinup plugin list --json` |

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `plugin "x" is not declared` | Run `spinup plugin list` for the names. |
| `plugin verify` exits with 1 | The source changed. Review it, then run `spinup plugin update <name> --trust`. |
| The command asks for `--trust` | No terminal is available. Pass `--trust`. |
| A Git source is rejected | Use `https`, `ssh`, or `git`, and pass `--version <tag>`. |

## See also

- [plugin new — scaffold a plugin](plugin-new.md)
- [Plugins](../plugins.md)
- [Executable plugins](../plugins-exec.md)
- [Plugin trust](../policy/plugins.md)
- [Flag reference: plugin](../site/commands/plugin.md), [add](../site/commands/plugin-add.md), [update](../site/commands/plugin-update.md), [verify](../site/commands/plugin-verify.md)
