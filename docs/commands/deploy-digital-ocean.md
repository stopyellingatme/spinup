---
title: deploy digital-ocean — create a server and deploy
parent: Command guides
---

# deploy digital-ocean — create a server and deploy

`spinup deploy digital-ocean` takes an app from no server to a live deploy on DigitalOcean in one command.
It signs in, creates a droplet, a firewall, and a DNS record, makes the secrets, commits the new files, and runs the normal [`spinup deploy`](deploy.md).
The command has two aliases: `digitalocean` and `do`.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --region nyc3 --no-recovery --yes
Region: nyc3
Droplet: create app-production, size s-2vcpu-4gb ...
...
Deployed release 20261022T080000Z-0123456789ab to production (green color)
```

If a run fails, run the same command again. It continues where it stopped.
After the first run, plain `spinup deploy` needs no DigitalOcean token.

{: .note }
This page is the command guide.
The full walkthrough, with the prerequisites, the firewall, and troubleshooting, is [Deploy to DigitalOcean](../deploy-digitalocean.md).

The command needs the network, so it refuses `--offline`:

```console
$ spinup deploy digital-ocean status --offline
Error: Use DigitalOcean
  the command needs the network, and --offline is set
  Try: run the command without --offline
[exit 2]
```

## Before you start

- Commit your work, or pass `--allow-dirty`.
- Choose a recovery key for the secrets, or pass `--no-recovery`. See [secrets](secrets.md).
- Expect one price question before the command creates a droplet. Pass `--yes` to answer it. With no terminal and no `--yes`, the command fails and names `--yes`.

## Preview the run

Add `--plan` to print what the command would create and change nothing.
The global `--dry-run` flag does the same.
The subcommands `login`, `logout`, and `destroy` have no preview and refuse `--dry-run`.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --env staging --plan
```

## Choose the droplet

`--region`, `--size`, and `--image` set the droplet.
The region defaults to the stored value, then `nyc3`.
The size defaults to `s-2vcpu-4gb` for the `production` environment and `s-1vcpu-2gb` for others.
The image must be an Ubuntu image.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --size s-2vcpu-4gb --region fra1 --image ubuntu-24-04-x64
```

## Use your own domain

Without `--domain`, the app gets an address that ends in `.sslip.io`.
Pass `--domain` to use your own host name.
If DigitalOcean holds the DNS zone, the command creates the A record.
If another provider holds it, the command prints the record and waits up to `--dns-timeout` (default 10 minutes).

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --domain app.example.com --dns-timeout 20m
```

`--public-url` sets a public URL that differs from `https://<domain>`.
If the public URL changes, you cannot use `--skip-build`, because the web image holds the public address.
A new `--domain` changes the public URL.

## Control who can reach the server

`--ssh-key` names the public key file of the operator.
`--ssh-from` limits port 22 to a comma list of sources, such as your office address.
The default is every address.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --ssh-key ~/.ssh/work.pub --ssh-from 203.0.113.7/32
```

## Make the secrets

The command runs `spinup secrets init` for you.
Pass the recovery recipient, or pass `--no-recovery` to accept that losing the environment key loses the secrets.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --recovery-recipient age1...
```

## Stop early

`--skip-deploy` stops before the deploy.
`--no-commit` does not commit the new files.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --skip-deploy --no-commit
```

## Pass deploy flags

These flags work as they do in [deploy](deploy.md): `--allow-dirty`, `--allow-destructive`, `--skip-build`, `--tag`, `--platform`, `--health-timeout`, and `--no-auto-rollback`.

## Sign in and out

`login` opens your browser, signs in, and stores the token.
It always starts a new sign-in, even when a token exists.
The browser must run on the same machine, because the answer returns to a loopback address.
With `--no-browser`, the command prints the URL and you open it yourself.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean login
Open this URL in a browser to sign in:
https://cloud.digitalocean.com/v1/oauth/authorize?...
Signed in to DigitalOcean as you@example.com (team My Team)
```

`logout` revokes the stored token and deletes the token file.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean logout
```

On a machine with no browser, and in CI, set `DIGITALOCEAN_ACCESS_TOKEN` instead.
spinup never stores that token.

## Check the status

`status` shows where the token comes from, the account, and the resources of the environment.
Outside a project, it shows the account only.
Add the global `--json` flag for JSON output.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean status --env staging
Token source: store (access token expires 2026-10-22T09:00:00Z)
...
```

## Replace the droplet

`--recreate` deletes the droplet and creates a new one.
If the droplet holds a release, the data is lost, so you must add `--i-know`.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --recreate --i-know
```

## Delete everything

`destroy` deletes the droplet, the firewall, and the DNS record of the environment.

{: .warning }
The database on the droplet is lost. Run `spinup backup` first.

```console
$ spinup deploy digital-ocean destroy
Error: destroy the environment
  the command deletes the droplet and its database
  Try: pass --i-know
[exit 1]
```

A protected environment also asks for confirmation. Pass `--confirm-env <name>` when you have no terminal.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean destroy --env staging --i-know --confirm-env staging
```

## Flags

| Group | Flags | Example |
| --- | --- | --- |
| Droplet | `--region`, `--size`, `--image` | `--region fra1` |
| Domain | `--domain`, `--public-url`, `--dns-timeout` | `--domain app.example.com` |
| Access | `--ssh-key`, `--ssh-from`, `--no-browser` | `--ssh-from 203.0.113.7/32` |
| Secrets | `--recovery-recipient`, `--no-recovery` | `--no-recovery` |
| Control | `--plan`, `--skip-deploy`, `--no-commit` | `--plan` |
| Replace | `--recreate`, `--i-know` | `--recreate --i-know` |
| Deploy | `--allow-dirty`, `--allow-destructive`, `--skip-build`, `--tag`, `--platform`, `--health-timeout`, `--no-auto-rollback` | `--allow-dirty` |

## See also

- [Deploy to DigitalOcean](../deploy-digitalocean.md): the full walkthrough
- [deploy](deploy.md), [secrets](secrets.md), and [rollback](rollback.md)
- Flag reference: [deploy digital-ocean](../site/commands/deploy-digital-ocean.md), [login](../site/commands/deploy-digital-ocean-login.md), [logout](../site/commands/deploy-digital-ocean-logout.md), [status](../site/commands/deploy-digital-ocean-status.md), [destroy](../site/commands/deploy-digital-ocean-destroy.md)
- Design record: [DigitalOcean deploy](https://github.com/stopyellingatme/spinup/blob/main/docs/adr/0017-digitalocean-deploy.md)
