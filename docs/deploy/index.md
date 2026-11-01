---
title: Deploy
nav_order: 4
has_children: true
has_toc: false
---

# Deploy

spinup puts your app on one VM that runs Docker Compose and Caddy.
Every deploy builds new images, runs the migrations, and switches traffic with no downtime.
Pick the path that fits how you get your server.

<!-- doctest: skip -->
```console
$ spinup deploy --env staging
Building release 20261015T080000Z-0123456789ab
...
Deployed release 20261015T080000Z-0123456789ab to staging (green color)
```

## Which path do I take?

| I want to ... | Read |
| --- | --- |
| Get a new server and an app on it with one command | [Deploy to DigitalOcean](../deploy-digitalocean.md) |
| Use a VM that I already have (any Debian or Ubuntu host) | [Deploy to your own server](../deploy.md) |
| Create a Hetzner Cloud server with OpenTofu | [Hetzner Cloud with OpenTofu](../infra.md) |
| Set the smoke URLs, replicas, and limits of an environment | [Environments](environments.md) |
| Keep passwords and keys out of git | [Secrets](secrets.md) |
| Deploy from GitHub Actions | [Generated CI](../ci.md) |

All three paths end in the same command, `spinup deploy`.
The DigitalOcean and Hetzner paths only create the server for you first.

## How a deploy works

`spinup deploy --env staging` does these five steps:

1. **Build.** It builds the `api` and `web` images. The image tag is the release ID, never `latest`.
2. **Ship the images.** It copies the images to the VM over SSH and uploads the release files and the secrets.
3. **Migrate.** It runs the database migrations while the old version still serves traffic.
4. **Switch.** It starts the new version next to the old one. This is the blue/green switch. When the new version is healthy, Caddy sends traffic to it. Then spinup stops the old version.
5. **Smoke check.** It requests `/healthz`, `/version`, and your smoke URLs through the public URL.

A deploy starts with checks. It stops if the git tree has uncommitted changes. Commit first, or pass `--allow-dirty`.
If the app has the `backups` module, a backup runs after the build and before the images ship. The first deploy skips it.
Use `--skip-backup` only when every pending migration is safe. See [Backups](../commands/backup.md).

If the new version is not healthy, the old version stays live and the deploy fails.
If a deploy stops half way, run `spinup deploy --resume`.
To go back to the last release, run `spinup rollback`.
Both commands are in the [deploy guide](../commands/deploy.md) and the [rollback guide](../commands/rollback.md).

{: .note }
A rollback does not undo database migrations. Run `spinup backup` before a risky migration.

## See also

- [Environments](environments.md): `staging`, `production`, and the settings of each
- [Secrets](secrets.md): the encrypted files that a deploy uploads
- [Monitoring](../monitoring.md): the dashboards that a deploy starts
- [Flag reference of `spinup deploy`](../site/commands/deploy.md)
