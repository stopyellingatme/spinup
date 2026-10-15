# Plugin packs and generators

Tier-1 plugins contain templates and declarative generators.
The CLI requires an approved source and hash before it renders plugin files.
Read the [trust policy](policy/plugins.md) before approving a plugin.

Run `spinup plugin add path:./plugins/example --trust` inside an existing app.
Commit `spinup.toml`, `.spinup/plugins.allow`, and `.spinup/plugins.history` together.
The approval command leaves `spinup.lock` unchanged.
Then run `spinup upgrade --yes` to create the plugin files on an upgrade branch.
Inspect the branch before you merge it.

For a new app, use `spinup new demo --plugin path:./plugins/example@1.0.0 --trust`.
Repeat `--plugin` to select multiple plugins.
The version suffix is optional for a local source.
A git source requires a fixed tag, such as `git:https://host/owner/repository@v1.0.0`.
A local source is resolved from the directory where you run `spinup new`.
The new declaration, allowlist, and history store that source relative to the new app.
For example, `path:./plugins/example` becomes `path:../plugins/example` when the app is a sibling of `plugins/`.
Preserve this directory layout when you clone or move the app.
Inside an existing app, `plugin add path:./plugins/example` resolves the path from the app directory.

Without `--trust`, an interactive terminal asks for approval after it displays the source, hash, and files.
Without a terminal, the command requires `--trust`.
The command validates all output paths before it creates the app directory.
Plugin declarations remain user configuration during template upgrades.
Each plugin pack must have a unique name.
Enable any core packs in its `requires` list before rendering it.

```console
$ spinup new --help
Create an application from template packs
...
$ spinup plugin add --help
Inspect and trust a plugin without rendering files
...
```

Run `spinup plugin update example --version 2.0.0 --trust` to approve a new version.
Then run `spinup upgrade --plan` to inspect the changes.
The upgrade also works when the core template version stays unchanged.
OLD uses the previously rendered plugin version.
NEW uses the newly approved plugin version.
Separate user edits merge through the same algorithm as core files.
Overlapping edits produce conflicts for review in the upgrade worktree.

Keep the cache for each previously rendered plugin version.
An unavailable OLD plugin stops the upgrade with a `Try:` instruction.
The upgrade never substitutes NEW plugin content for OLD content.
During upgrade and generation, `--offline` requires approved plugin data in the cache.
A hash mismatch also stops rendering.

The lock records plugin templates with the `plugin/<name>/` prefix.
The lock records only the plugins already rendered into the app.
The declaration selects NEW versions, and the allowlist approves their hashes.
The history file preserves previous approvals for OLD versions.
Upgrade verifies OLD against both the lock and history before rendering it.
After upgrade, commit the resulting lock with the rendered files.

Declarative generators become available inside the project.
For the example plugin, run `spinup generate greeting Ada`.
Use `--dry-run` to list files or `--diff` to inspect their contents.
Use `--force` to approve an overwrite of different content.
The normal generator runner applies files and records their classes and hashes.
Plugin generators record `plugin/<name>/@gen/<generator>` as their template.
Pack sources cannot start with the reserved `@gen/` directory.
Upgrades still recognize the earlier `plugin/<name>/gen/<generator>` form.
Pack sources under `gen/` retain pack ownership.
Core upgrades retain these generated files.

Generator declarations use named positional arguments.
Their output paths, source paths, and template contents use Go templates with `{{` and `}}` delimiters.
The pack templates use the core delimiters `[[` and `]]`.
The CLI rejects unsafe output paths and duplicate generator names.
Generated Go files receive the standard Go format.

Plugin questions must declare a default, including an explicit empty default when appropriate.
The CLI rejects questions without defaults during add, update, and verify.
Upgrade stores completed answers in the next lock.
A later default change retains the recorded answer.

Run `spinup plugin remove example` to remove its declaration and current approval.
The next upgrade deletes unmodified pack files and preserves edited files as orphans.
Generator files retain their ownership.
Inspect these changes with `spinup upgrade --plan`.

If Git ignores the trust records, replace `.spinup/` in `.gitignore` with these lines:

```gitignore
.spinup/*
!.spinup/plugins.allow
!.spinup/plugins.history
```

The add and update commands warn when Git ignores either record.
Commit both records with the declaration.
