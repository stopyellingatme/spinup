---
title: Support policy
parent: Reference
nav_order: 8
---

# Support policy

This page says which versions of spinup get fixes, which tools and platforms work with it, and how far back you can upgrade.
The numbers here come from the same files that the CLI uses. To check them on your machine, run `spinup docs compat`.

```console
$ spinup docs compat
# Compatibility
...
```

If a number on this page and the output of that command differ, the command is right. Please report the page as a bug.

## Supported versions

spinup supports the last two minor versions of the CLI and the kit.
If 0.5 is the newest minor, then 0.5 and 0.4 are supported.
A security fix goes to both. Other fixes go to the newest minor only.
To report a vulnerability, follow [SECURITY.md](https://github.com/stopyellingatme/spinup/blob/main/SECURITY.md).

## Releases

- A minor version ships each month.
- A patch version ships when a fix is ready.
- The [changelog](https://github.com/stopyellingatme/spinup/blob/main/CHANGELOG.md) has a "Breaking" section and an "Upgrade steps" section for each release.
- One release publishes the CLI as `vX.Y.Z` and the kit as `kit/vX.Y.Z`.

Run `spinup version --check` to see if a newer release exists.

## Keep the CLI and the kit in step

The CLI and the kit must have the same major and minor version. Patch versions can differ.
`spinup doctor` warns when they differ, because an old kit can lack what a new template needs.

<!-- doctest: skip -->
```console
$ spinup doctor
...
warn	kit-version	CLI 0.5.1 is incompatible with kit 0.4.2: major and minor versions must match
Try: spinup upgrade or go get github.com/stopyellingatme/spinup/kit@v0.5.x
```

Run `spinup upgrade` to fix it. See [Upgrade](../upgrading.md).
The kit states only minimum versions of its own dependencies, so your app still controls `go get -u`.

## Kit stability

Until version 1.0, the kit stays on v0.x. Version 1.0 needs two minors in a row with no breaking change to the kit.
After 1.0, a breaking change needs a new major version and a codemod. A codemod is a script that edits your code for you.

A deprecated kit API stays for one minor. During that time:

- The doc comment says `Deprecated:` and names the replacement.
- The app logs one warning at startup if it uses the deprecated path.

The next minor can remove the API. A new default behavior never changes an existing app.
You opt in with `behavior_version`. See [Kit compatibility](kit-compat.md) and [Behavior versions](behavior-version.md).

## Tools and platforms

| Item | Supported |
| --- | --- |
| Go | 1.26 and 1.27. A generated app declares `go 1.26.0`. CI tests both. |
| Node | 24. The generated image uses `node:24-bookworm-slim`. |
| pnpm | The pinned version, through corepack. Bun is an option for the web app. |
| PostgreSQL for a new app | 16, 17, and 18. The default is 18. Pick it with `--db-version`. |
| PostgreSQL for `spinup db doctor` | 14 to 18. It warns for 14, which ends on 2026-11-12. It fails for 13 and older. |
| Operating system | Linux and macOS on amd64 and arm64. Windows runs some commands natively. See [Windows](windows.md). |

The list of PostgreSQL versions in the [compatibility matrix](../site/compat.md) is the list that checks accept. It is longer than the list for new apps.
The same data pins the container images. See the [pinned stack](../site/stack.md).

## How far back you can upgrade

The CLI carries the template files of the last three releases inside the program.
For an older release, `spinup upgrade` downloads the files from the signed release assets.
That needs the network, or a cache that you filled with `spinup cache warm`. With `--offline`, only the built-in and cached versions work.

If your app is more than one minor behind, `spinup upgrade` upgrades in steps. It uses the latest patch of each minor on the way.
It makes one commit for each step. Run `--plan` first to see every step:

<!-- doctest: skip -->
```console
$ spinup upgrade --plan
Upgrade 0.3.2 -> 0.4.3
overwrite: 2
  .github/workflows/ci.yml
...
Upgrade 0.4.3 -> 0.5.1
...
Kit behavior 2 (opt in with behavior_version = 2 in spinup.toml): ...
```

If the files of a middle minor are not available, the upgrade stops before it changes a file.
spinup tests upgrades from every release that it supports, and from the oldest of them in a chain.
For an app that is older than that, upgrade to the oldest supported minor first.

spinup does not downgrade an app. A template version needs a CLI that is at least as new as the minimum that the template names.

## Plugins

Plugins have their own API version. Plugins that run programs have the full rights of your user account.
Read [Plugin trust](plugins.md) before you approve one.

## See also

- [Upgrade](../upgrading.md): the steps to move an app to a new version
- [Kit compatibility](kit-compat.md): what a kit change can do to your app
- [Behavior versions](behavior-version.md): opt in to new defaults
- [Compatibility matrix](../site/compat.md) and [Pinned stack](../site/stack.md): the exact versions
- [The upgrade command guide](../commands/upgrade.md): the flags
