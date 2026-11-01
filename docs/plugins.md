---
title: Plugins
parent: Build your app
nav_order: 15
---

# Plugins

A plugin adds your own files and generators to a spinup app.
For example, a plugin can add a `greeter-note Ada` command that writes `notes/Ada.txt`, or a template for a service in another language.
This page covers the common kind, the plugin pack, which is data only.
spinup never runs code from a pack.
If you need code, for example to add a doctor check, see [Executable plugins](plugins-exec.md).

spinup renders a plugin only after you approve its exact contents.
The approval stores a hash, so a changed plugin stops until you approve it again.
Read the [trust policy](policy/plugins.md) for the details.

Create a plugin, add it to an app, and run its generator:

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ spinup plugin new greeter
...
$ cd todo
$ spinup plugin add path:../greeter --trust
Name: greeter
...
Next: spinup upgrade --plan
$ spinup g greeter-note Ada
Created 1, updated 0 files
$ cat notes/Ada.txt
Note: Ada
```

## Add a plugin to an app

`plugin add` shows the source, the hash, and the files the plugin would write.
It changes your configuration, not your code.

| Source | Example |
| --- | --- |
| A local directory | `spinup plugin add path:../greeter --trust` |
| A git tag | `spinup plugin add git:https://github.com/acme/greeter --version v1.0.0 --trust` |

A local path is relative to the app directory.
A git source needs a fixed tag, not a branch. Pass the tag with `--version`.
Without `--trust`, a terminal shows the details and asks you to approve.
Without a terminal, the command needs `--trust`.

The command changes three files. Commit them together:

| File | What it holds |
| --- | --- |
| `spinup.toml` | The `[[plugins]]` declaration with the name, source, and version. |
| `.spinup/plugins.allow` | One line for each approved plugin: name, source, and hash. |
| `.spinup/plugins.history` | Every approved version, so an upgrade can still verify older versions. |

```toml
[[plugins]]
name = 'greeter'
source = 'path:../greeter'
version = '0.1.0'
```

`plugin add` does not render files and leaves `spinup.lock` unchanged. Run `spinup upgrade --plan` to see what the plugin adds.
Then run `spinup upgrade --yes` to create the files on an upgrade branch.
Inspect the branch before you merge it.

{: .tip }
Generated apps already allow `.spinup/plugins.allow` and `.spinup/plugins.history` in `.gitignore`. If your app is older and ignores them, `plugin add` warns. Replace `.spinup/` in `.gitignore` with `.spinup/*`, then add `!.spinup/plugins.allow` and `!.spinup/plugins.history`.

### Add a plugin to a new app

Use `--plugin` on `spinup new`. Repeat it for more plugins.

<!-- doctest: skip -->
```console
$ spinup new demo --plugin path:./plugins/greeter@1.0.0 --trust
```

The `@version` suffix is optional for a local path. For a git source, write the tag: `git:https://github.com/acme/greeter@v1.0.0`.
The local path is relative to the directory where you run `spinup new`. The app stores it relative to the app.
So `path:./plugins/greeter` becomes `path:../plugins/greeter` when the app sits next to `plugins/`.
Keep that layout when you clone or move the app.

## Use its generators

Each generator of a plugin becomes a `spinup generate` command.

```console
$ spinup g greeter-note Grace --dry-run
create notes/Grace.txt (scaffold)
```

`--dry-run` lists the files. `--diff` shows their contents. `--force` replaces a file that has different content.
The generator records each file in `spinup.lock`, so a later core upgrade keeps your files.

## See what is installed

| Command | What it shows | Example |
| --- | --- | --- |
| `spinup plugin list` | The declared plugins and their versions. | `spinup plugin list` |
| `spinup plugin info <name>` | The verified metadata, generators, and files. | `spinup plugin info greeter` |
| `spinup plugin verify` | Fetches each plugin again and checks the hash. | `spinup plugin verify` |

```console
$ spinup plugin list
...
$ spinup plugin verify
...
```

## Update a plugin

Approve the new version, then upgrade.
Two words matter in the upgrade: OLD is the version already in your app, and NEW is the version you just approved.
spinup merges your edits into the NEW files in the same way as it does for core files.
If your edits overlap with the plugin change, you review the conflicts in the upgrade worktree.

<!-- doctest: skip -->
```console
$ spinup plugin update greeter --version 2.0.0 --trust
$ spinup upgrade --plan
$ spinup upgrade --yes
```

The upgrade needs the OLD version too, so keep the cache of each version you rendered.
The lock records the plugin templates with the prefix `plugin/<name>/`. The history file lets the upgrade verify the OLD version.
If the OLD version is missing, the upgrade stops with a `Try:` line.
It never uses NEW content in place of OLD content.
With `--offline`, the approved plugin data must already be in the cache. A hash mismatch also stops the render.

## Remove a plugin

```console
$ spinup plugin remove greeter
Removed plugin greeter. The next upgrade deletes unmodified pack files and preserves edited files as orphans.
Retained files until the next upgrade (review with spinup upgrade --plan):
  notes/Ada.txt
```

The command removes the declaration and the approval.
The next upgrade deletes the pack files you did not edit, and keeps the ones you edited.
Files that a generator wrote stay yours.

## Add a service template

A plugin can add a template for a [sidecar service](services.md).
After you add and trust the plugin, use `--lang <plugin>/<template>`:

<!-- doctest: skip -->
```console
$ spinup plugin add path:./plugins/service-example --trust
$ spinup g service ocr --lang service-example/go-http
create services/ocr/Dockerfile (scaffold)
...
```

The template is rendered with `Name`, `App`, and `Port`.
A `.tmpl` file is rendered and loses the suffix. Other files are copied.
A file with an execute bit, such as `entrypoint.sh`, gets mode 0755. Other files get mode 0644.
A `service.toml` file in the template sets `health` and `healthcheck` for the new `[[services]]` entry.
It is not copied into the service. Without it, the new service has no health check command.
spinup runs no plugin code for this. See [Sidecar services](services.md) for what a service is.

## Write your own plugin

`spinup plugin new <name>` creates a pack plugin you can edit.
Add `--kind exec` to create an [executable plugin](plugins-exec.md) instead.
A pack plugin has these files:

| File | Purpose |
| --- | --- |
| `plugin.toml` | The name, version, and generators. |
| `pack.toml` | The pack name and description. |
| `files/` | The templates. |
| `README.md` | Notes for people who use your plugin. |

This `plugin.toml` defines the `greeter-note` generator:

```toml
name = "greeter"
version = "0.1.0"
api = 1
spinup = ">=0.0.0"
description = "Templates for greeter."

[[generators]]
name = "greeter-note"
short = "Create a note"
usage = "greeter-note <Name>"
args = ["Name"]

[[generators.files]]
path = "notes/{{.Name}}.txt"
source = "files/note.txt.tmpl"
class = "scaffold"
```

The file `files/note.txt.tmpl` holds `Note: {{.Name}}`.

- Each argument in `args` becomes a name that you can use in `path` and in the template, written `{{.Name}}`.
- The `class` is `managed`, `scaffold`, or `seed`. If you are not sure, use `scaffold`.
- Pack files outside `[[generators]]` use `[[` and `]]` as delimiters, to avoid a clash with Go templates.
- A plugin cannot write to `.git`, `.spinup`, `.github`, `spinup.toml`, or `spinup.lock`.
- A question in a plugin needs a default. An empty default is fine. A later change of the default keeps the answer in `spinup.lock`.
- A pack name must be unique. If the pack lists core packs in `requires`, enable them in the app first.

Edit `files/note.txt.tmpl`, then run `spinup plugin add` on your app to try it.
Go files that a generator writes get the standard Go format.
See [Create a plugin](commands/plugin-new.md) for every flag.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `plugin add` says the command needs `--trust` | There is no terminal to ask you. | Pass `--trust` after you read the source and hash. |
| A plugin stops with a hash mismatch | The plugin files changed after approval. | Run `spinup plugin update <name> --trust` after you review them. |
| The upgrade cannot find the OLD plugin | The cache has no copy of the rendered version. | Follow the `Try:` line, or restore the old version. |
| A question is rejected | It has no default. | Add a `default` to the question. |

## See also

- [Executable plugins](plugins-exec.md) for plugins that run code
- [Plugin trust](policy/plugins.md)
- [Sidecar services](services.md)
- [Plugin commands](commands/plugin.md) and the [flag reference](site/commands/plugin.md)
