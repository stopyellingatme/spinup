---
layout: default
title: spinup
---

# spinup

spinup creates and operates a full-stack application: a Go API, a SvelteKit web app, PostgreSQL, a Docker Compose deploy to a VM, and OpenTelemetry monitoring.

## Quick start

Install spinup with the script `install.sh` of the release, or with the Homebrew cask `spinup`.
The script takes `--version X.Y.Z` and `--dir DIR`. The default directory is `~/.local/bin`.
Then create an app and start it.

<!-- doctest: skip -->
```console
$ spinup new todo
$ cd todo
$ spinup doctor
$ spinup dev
```

`spinup doctor` checks your tools and your files.
`spinup dev` starts PostgreSQL, the API, the web app, and Mailpit.
Open `http://localhost:5173` for the app and `http://localhost:8080` for the API.

## Next

- Generate a resource: run `spinup g resource` (see the [command reference](site/commands/index.md)).
- Add a service: run `spinup g service <name> --lang rust|python|node`. See [Sidecar services](services.md).
- Deploy: run `spinup deploy digital-ocean` for a new droplet, or see [Production deploy files](deploy.md) for your own VM.

## Reference

- [Reference home](site/index.md)
- [Command reference](site/commands/index.md)
- [Compatibility matrix](site/compat.md)
- [Pinned stack](site/stack.md)

## Build

- [The web app](web.md)
- [Select the web tools](web-variants.md)
- [Embed mode](embed.md)
- [API reference](api-docs.md)
- [Accounts and sessions](auth.md)
- [Mail and password reset](mail.md)
- [Sidecar services](services.md)
- [Plugin packs and generators](plugins.md)
- [Executable plugins](plugins-exec.md)

## Deploy

- [Production deploy files](deploy.md)
- [Deploy to DigitalOcean](deploy-digitalocean.md)
- [Hetzner Cloud infrastructure](infra.md)
- [Generated CI](ci.md)

## Operate

- [Production monitoring files](monitoring.md)
- [Logical backups with pgdump](backups-pgdump.md)
- [WAL-G backups](backups-walg.md)
- [Application telemetry and data handling](data-handling.md)
