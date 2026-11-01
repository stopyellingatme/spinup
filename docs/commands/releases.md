---
title: releases — list and prune releases
parent: Command guides
---

# releases — list and prune releases

Each deploy puts a release on the VM of an environment.
`spinup releases` lists those releases, shows one, and removes old ones.
All three need `--env`, because the history lives on the VM.

<!-- doctest: skip -->
```console
$ spinup releases list --env staging
CURRENT ID GIT_SHA TIME USER MIGRATION
* 20261015T080000Z-0123456789ab 0123456789ab 2026-10-15T08:00:00Z ana 00008
- 20261014T080000Z-ffffffffffff ffffffffffff 2026-10-14T08:00:00Z ana 00007
```

```console
$ spinup releases list --help
...
```

## List releases

`spinup releases list --env staging` prints the history.
The newest release comes first. A `*` marks the release that runs now.
The columns are the release ID, the Git commit, the time, the user, and the migration version.
`spinup releases --env staging` does the same.
Add `--json` for structured output.

<!-- doctest: skip -->
```console
$ spinup releases list --env staging --json
{
  "current": "20261015T080000Z-0123456789ab",
  "releases": [...]
}
```

## Show one release

`spinup releases show <id> --env staging` prints the snapshot of one release as JSON.
The snapshot lists the images, the migration version, and the user.
It holds hashes of the secrets, never the values.
The command refuses a snapshot with an invalid hash.

<!-- doctest: skip -->
```console
$ spinup releases show 20261015T080000Z-0123456789ab --env staging
{
  "release": "20261015T080000Z-0123456789ab",
  "git_sha": "0123456789abcdef...",
  "migration_version": "00008",
  "secrets": {"DATABASE_URL": "9f2c..."},
  ...
}
```

## Prune old releases

`spinup releases prune --env staging` removes old release directories from the VM.
`--keep <n>` sets how many to retain (default 5).
The command always keeps the current and the previous release, so you can still [roll back](rollback.md).
So it can keep more than `--keep` says.
It takes the deploy lock, and a protected environment asks for confirmation.

<!-- doctest: skip -->
```console
$ spinup releases prune --keep 3 --env staging
Kept 3 releases, removed 4
```

{: .warning }
Pruning deletes the release directories on the VM. You cannot roll back to a pruned release.

Docker images stay on the VM, and `spinup maintenance` cleans them up later.

```console
$ spinup releases prune --keep 0 --env staging
...
  Try: use releases list, show <release-id>, or prune --keep <positive number>
[exit 1]
```

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `could not resolve environment` | Pass `--env <name>`. Check the names in `spinup.toml`. |
| `current is missing from the history` | Inspect `releases.json` on the VM before you prune. |
| An SSH error | Check the connection and the host key. See [deploy](deploy.md). |

## See also

- [rollback](rollback.md)
- [deploy](deploy.md)
- [Flag reference: releases](../site/commands/releases.md), [list](../site/commands/releases-list.md), [show](../site/commands/releases-show.md), [prune](../site/commands/releases-prune.md)
