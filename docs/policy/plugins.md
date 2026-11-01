---
title: Plugin trust
parent: Reference
nav_order: 11
---

# Plugin trust

A plugin adds files and generators to your app. Its files become part of your code base, so spinup asks you to approve each plugin before it uses it.
This page explains what you approve, which files record the approval, and what spinup checks for you.

```console
$ spinup new todo --api-only --module example.com/todo --modules '' --no-git --non-interactive
...
$ spinup plugin new notes --out plugins/notes
...
$ cd todo
$ spinup plugin add path:../plugins/notes --trust
Name: notes
...
Next: spinup upgrade --plan
```

## The short version

- A pack plugin has templates and declarative generators. spinup never runs its code and never installs its dependencies.
- You approve one exact copy of the plugin. spinup stores a hash of the files. If one byte changes, spinup stops and asks again.
- Read the files before you approve. A generated file can change how your app behaves.
- An executable plugin runs a program with the full rights of your user account. Approve it only if you trust its author. See [Executable plugins](../plugins-exec.md).

## Add and approve a plugin

`spinup plugin add` shows the name, version, source, hash, files, and generators. Then it asks you to approve.
In a terminal, you type `yes`. Without a terminal, you must pass `--trust`. The global `--yes` flag does not approve a plugin.

```sh
spinup plugin add path:./plugins/notes --trust
spinup plugin add git:https://github.com/example/notes-plugin --version v1.0.0 --trust
spinup new demo --plugin path:./plugins/notes --trust
```

A Git source needs a fixed tag. A branch name is not accepted, because a branch can move.

The command writes three things and renders no files:

| File | What it holds |
| --- | --- |
| `spinup.toml` | The plugin declaration: name, source, and version. |
| `.spinup/plugins.allow` | One line for each approved plugin: the name, the source, and the hash. |
| `.spinup/plugins.history` | The hashes that you approved before. Upgrades use it to find the old files. |

An allowlist line looks like this:

```text
notes path:../plugins/notes de39dffd74385c8de16b098d108402472fdad150287900655461cc32a32871de
```

A line for an executable plugin ends with the word `exec`. A line without it cannot approve execution.

## Commit the records

Commit `spinup.toml`, `.spinup/plugins.allow`, and `.spinup/plugins.history` together. Your team and your CI then use the same approval.
A new app already has the right lines in `.gitignore`:

```console
$ grep -n 'spinup' .gitignore
...
```

```gitignore
.spinup/*
!.spinup/plugins.allow
!.spinup/plugins.history
```

If your `.gitignore` hides the records, `plugin add` and `plugin update` print the lines to change.
Check the records in CI with `spinup plugin verify`. It fetches each source again and fails if a hash differs.

## When a plugin changes

If the files of a plugin change, the hash no longer matches and spinup stops:

```console
$ echo 'extra line' >> ../plugins/notes/files/note.txt.tmpl
$ spinup plugin verify
...
  Try: inspect the plugin source, then run spinup plugin add or update --trust
[exit 1]
```

The first line of the error shows both hashes:

```text
Error: resolve plugin: plugin "notes" hash mismatch: got af07d478f0fa..., approved de39dffd7437...
```

Look at the change, then approve it. The command prints the old and the new hash:

```console
$ spinup plugin update notes --trust
...
Next: spinup upgrade --plan
```

The first line of the output is `Hash: <old hash> -> <new hash>`.

Then run `spinup upgrade --plan` to see which files change in your app. A plugin edit and your own edit merge the same way as a core file.
If both edit the same lines, you get a conflict to resolve in the upgrade worktree.

If you change the contents of a plugin, change its version too. One version must not have two hashes.

## What an upgrade does with plugins

The upgrade needs the old files of each plugin as well as the new ones, so it can merge your edits. It finds the old files in the cache.
If the old files are not there, the upgrade stops with a `Try:` line. It never uses the new files in place of the old ones.
Keep the cache folder if you want upgrades that you can repeat. To refill it, run `spinup plugin add` or `spinup plugin update` again.
A cache that an older spinup version wrote does not count. Approve the current source again to fill the new cache.

With `--offline`, spinup uses only plugins that are in the cache and approved.

To remove a plugin, run `spinup plugin remove notes`. The next upgrade deletes its unchanged files. It keeps the files that you edited.

## What spinup checks

| Check | Limit |
| --- | --- |
| Size of a plugin | 5 MiB and 1000 entries. |
| Links and special files | Symbolic links, submodules, and special files are rejected. |
| Paths | A plugin cannot write to `.git`, `.spinup`, `.github`, `spinup.toml`, or `spinup.lock` at the root of the app. It cannot use a `.git` segment at any depth. Case does not matter. |
| Git download | A shallow clone of one fixed tag. No submodules. A time limit of two minutes. |
| Git download size | spinup stops the clone above 20 MiB or about 4000 entries, and it removes the temporary folder. |
| Git settings | spinup ignores your global Git config and Git hooks. It does not prompt for credentials. Use an SSH agent for a private repository. |
| Questions | Each question of a plugin needs a default, because an upgrade cannot ask. An upgrade stores the answers in `spinup.lock`. |
| `spinup` range | A plugin states the spinup versions that it works with, such as `>=0.5.0 <0.7.0`, `^0.5.0`, `~0.5.2`, or `^0.5.0 || ^0.6.0`. A release build must satisfy it. A development build checks only the syntax. |

## Executable plugins

An executable plugin adds generators and doctor checks through a program that spinup starts.
It has the full rights of your user account, and spinup does not sandbox it.
Approve it with `--trust-exec <name>`, or type the plugin name in a terminal. The `--trust` flag cannot approve it.
Read [Executable plugins](../plugins-exec.md) before you do.

## For plugin authors

- Use `spinup plugin new <name>` to start a plugin. See [Plugin guide](../plugins.md).
- Put a unique name in each pack. Do not use a source folder that starts with `@gen/`. That name is reserved for generator records.
- Use `[[` and `]]` in pack templates. Use `{{` and `}}` in generator paths and templates.
- Keep local plugin sources next to the app. spinup stores a local source as a path relative to the app, so keep the layout when you move the app.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `cannot trust plugin without a terminal` | There is no terminal and no `--trust`. | Check the plugin, then pass `--trust`. |
| `hash mismatch` | The files of the plugin changed after you approved them. | Look at the change. Run `spinup plugin update <name> --trust`. |
| The upgrade says an old plugin version is missing | The cache has no old files. | Run `spinup plugin add` again for the old version, or upgrade with network access. |
| `plugin "x" is not declared` | The name is not in `spinup.toml`. | Run `spinup plugin list`. |
| CI cannot find the approval | The records are not in Git. | Fix `.gitignore` and commit `.spinup/plugins.allow` and `.spinup/plugins.history`. |

## See also

- [Plugins](../plugins.md): write and use pack plugins
- [Executable plugins](../plugins-exec.md): plugins that run a program
- [Plugin command guide](../commands/plugin.md) and [plugin add](../site/commands/plugin-add.md): the commands and flags
- [Support policy](support.md): the plugin API version
