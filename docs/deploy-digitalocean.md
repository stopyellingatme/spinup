---
title: Deploy to DigitalOcean
parent: Deploy
nav_order: 1
---

# Deploy to DigitalOcean

One command creates a droplet, a firewall, and a DNS record, makes the secrets, and deploys your app.
The command is `spinup deploy digital-ocean`. The aliases `digitalocean` and `do` work too.

<!-- doctest: skip -->
```console
$ age-keygen -o ~/recovery.agekey      # prints your recovery recipient: age1...
$ git add -A && git commit -m "ready to deploy"
$ spinup deploy digital-ocean --recovery-recipient age1...
```

spinup prints the plan, asks one question, and then does the work.
If a step fails, run the same command again. Each run continues where the last one stopped.
Without `--env`, the environment is `default_env` of `spinup.toml`, or else `production`.

This page covers the DigitalOcean path.
For a server you already have, read [Deploy to your own server](deploy.md).
For Hetzner, read [Hetzner Cloud with OpenTofu](infra.md).

## Before you start

- A spinup project with the `deploy` module. `spinup new` adds it by default.
- These tools on your machine: `ssh`, `ssh-keygen`, `sops`, `age-keygen`, `git`, and `docker`.
- An SSH public key. spinup reads `~/.ssh/id_ed25519.pub`. Pass `--ssh-key <file>` to use another key.
- A clean git tree. Commit first, or pass `--allow-dirty`. The `deploy/environments/<env>.toml` file that spinup creates does not count as a change.

## Sign in

The first run in a terminal opens your browser and asks you to sign in.
You can also sign in by itself:

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean login
Open this URL in a browser to sign in:
https://cloud.digitalocean.com/v1/oauth/authorize?...
Signed in to DigitalOcean as you@example.com (team My Team)
```

The browser sends the answer back to a loopback address on your machine, so the browser must run on the same machine.
If the browser does not open, copy the URL from the output, or pass `--no-browser`.

On a machine with no browser, and in CI, use a personal access token.
Create it at `https://cloud.digitalocean.com/account/api/tokens`:

<!-- doctest: skip -->
```console
$ export DIGITALOCEAN_ACCESS_TOKEN=<your token>
$ spinup deploy digital-ocean status
```

spinup reads `DIGITALOCEAN_ACCESS_TOKEN` first and `DIGITALOCEAN_TOKEN` second. It never stores these tokens.

The browser sign-in stores a token in `~/.config/spinup/credentials/digitalocean.json` (mode 0600).
Set `SPINUP_CREDENTIALS_DIR` to use another folder.
spinup refreshes the token before it expires.
To revoke the token at DigitalOcean and delete the file, run `logout`:

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean logout
```

## The first deploy

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --env staging --recovery-recipient age1...
```

The question names the size, the region, and the price for each month.
Answer it, or pass `--yes`. With no terminal and no `--yes`, the command fails and names `--yes`.

The command does these things, in this order:

1. Makes the missing resources: the droplet, the firewall, and the DNS record if you gave a domain.
2. Writes the environment to `spinup.toml` and makes the missing secrets.
3. Commits the new files.
4. Runs `spinup deploy`.

To see the plan and change nothing, add `--plan`:

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --env staging --plan
```

To stop before the deploy, add `--skip-deploy`.
To stop before the commit, add `--no-commit` and commit the files yourself:

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --env staging --skip-deploy
$ spinup deploy digital-ocean --env staging --no-commit --recovery-recipient age1...
```

The commit holds `spinup.toml`, `.sops.yaml`, `secrets/<env>.enc.yaml`, and a new `deploy/environments/<env>.toml`.
Its message is "spinup: provision <env> on DigitalOcean".

Without `--domain`, the app gets an address that ends in `.sslip.io`, and Caddy gets a Let's Encrypt certificate for it.
All sslip.io users share one Let's Encrypt limit, so use your own domain for a real app.

## Use your own domain

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --domain app.example.com --recovery-recipient age1...
```

If DigitalOcean DNS holds the zone, spinup creates or updates the A record.
It finds the zone by the longest matching suffix.
If another provider holds the zone, spinup prints the record to create and waits for it to resolve.
The wait lasts for `--dns-timeout` (10 minutes by default).
spinup warns you about an AAAA record that points to another host.

A new domain needs a new web image, so you cannot combine `--domain` with `--skip-build`.
The public URL is `https://<domain>`. Pass `--public-url` to set another value.

## Choose a size and a region

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --size s-2vcpu-4gb --region fra1 --image ubuntu-24-04-x64
```

| Flag | Default | Note |
| --- | --- | --- |
| `--size` | `s-2vcpu-4gb` for `production` (about $24 each month), else `s-1vcpu-2gb` | The size needs at least 2 GiB of memory and enough CPUs for `deploy/environments/<env>.toml`. |
| `--region` | the stored value, then `nyc3` | |
| `--image` | `ubuntu-24-04-x64` | Only Ubuntu images work. |

If a size or region is not available, the error names up to three sizes with their monthly price, and up to three regions that offer your size.
The `Try:` line shows the fix, for example `pass --size s-2vcpu-4gb ($24.00/month)`.

## Restrict SSH

The Cloud Firewall `spinup-<app>-<env>` allows 80/tcp, 443/tcp, and 443/udp from everywhere.
It allows 22/tcp from everywhere too, unless you pass `--ssh-from`:

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --ssh-from 203.0.113.4/32,198.51.100.0/24
```

Docker bypasses the ufw rules on the droplet, so the Cloud Firewall is the real filter.

## Secrets and the recovery key

If `secrets/<env>.enc.yaml` does not exist, spinup creates it.
You must pass `--recovery-recipient <age recipient>` or `--no-recovery`.
Without one of them, the command stops before it creates anything.
`--no-recovery` accepts that nobody can decrypt the file if every key is lost.

Make a recipient with `age-keygen`. Keep the private key outside the repository:

<!-- doctest: skip -->
```console
$ age-keygen -o ~/recovery.agekey
Public key: age1...
```

spinup makes only the secrets that are missing: `POSTGRES_PASSWORD`, `DATABASE_URL`, and `GRAFANA_ADMIN_PASSWORD` when monitoring is on.
It never replaces a value and never prints one.
See [Secrets](deploy/secrets.md) for how to manage them later.

## Other environments

`production` and `staging` have a settings file in a new app.
If you deploy another environment, for example `preview`, spinup copies `deploy/environments/staging.toml` to `deploy/environments/preview.toml` and prints `Created deploy/environments/preview.toml from staging`.
It never overwrites an existing file.
See [Environments](deploy/environments.md).

## Deploy again

After the first run, use plain `spinup deploy`.
It needs no DigitalOcean token, so your CI stays the same.
If a deploy fails, run `spinup deploy --resume`.
The flags `--allow-dirty`, `--skip-build`, `--tag`, `--health-timeout`, and `--no-auto-rollback` of `spinup deploy` also work on the first run.

## Check the status

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean status
Token source: store (access token expires 2026-10-22T09:00:00Z)
...
```

The command shows the token source, the account, the droplet, the firewall, and the DNS record.
It also lists differences between `spinup.toml` and DigitalOcean.
Add the global `--json` flag for JSON output.

## Replace or destroy the droplet

To delete the droplet and make a new one, add `--recreate`.
If a release exists, the data on the droplet is lost, so add `--i-know`:

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --recreate --i-know
```

To delete everything the command made, run `destroy`:

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean destroy --env staging --i-know
```

{: .warning }
The database on the droplet is lost. Run `spinup backup` first.

`destroy` asks you to type the environment name.
Without a terminal, pass `--confirm-env staging` instead. In CI, `CI=true` with `--yes` also works.

```sh
spinup deploy digital-ocean destroy --env staging --i-know --confirm-env staging
```

It deletes the droplet, the firewall, and the A record that points at the droplet.
It deletes only resources that carry the tag of the environment and the IDs in `spinup.toml`.
Then it deletes the tags `spinup:<app>:<env>` and `spinup` when no resource has them.
It keeps your account SSH key, the local secrets, and the age key.

## Costs

A droplet costs money for each hour until you destroy it.
If a run fails after the droplet exists, the command prints the droplet, the price for each hour, and the destroy command.
DigitalOcean backups and monitoring stay off.

## Host key pins

spinup makes an SSH host key on your machine before it creates the droplet.
It writes the public key to `host_keys` in `spinup.toml`, and cloud-init installs the key on the droplet.
So your first connection is already trusted.
spinup then replaces the key on the droplet, because DigitalOcean metadata can read the first one.
If a host key does not match, the command stops and never retries.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| Another run works on the environment | Only one `deploy digital-ocean` or `destroy` run can work on an environment. The lock is `.spinup/provision-<env>.lock`. | Wait until the other run ends. |
| cloud-init failed | The first boot of the droplet failed. The command prints the errors. | Run `spinup deploy digital-ocean --recreate` (add `--i-know` if a release exists). |
| Host key mismatch | Someone replaced the droplet, or something else answers at the address. | Check the droplet in the DigitalOcean console. Then run `--recreate` if you need a new one. |
| HTTP 422 on create | The account hit its droplet limit, or the size, region, or image is not available. | Delete a droplet or ask DigitalOcean for a higher limit. Otherwise run `--plan` and pass another `--size`, `--region`, or `--image`. |
| HTTP 403 | The token lacks a scope. The message names it. | Run `login` again, or create a token with that scope. |
| HTTP 401 | The token expired or DigitalOcean revoked it. | Run `login` again. |
| Wrong team | The token belongs to another team than the environment. | Sign in to the team that owns the environment. |
| The sign-in is not available in this build | The build has no registered client ID. | Use a personal access token, or set `SPINUP_DO_CLIENT_ID`. |

## See also

- [Deploy to your own server](deploy.md) for the files a deploy uses
- [Environments](deploy/environments.md) and [Secrets](deploy/secrets.md)
- [The `deploy digital-ocean` command guide](commands/deploy-digital-ocean.md)
- [Flag reference](site/commands/deploy-digital-ocean.md)
- Design record: [DigitalOcean deploy](https://github.com/stopyellingatme/spinup/blob/main/docs/adr/0017-digitalocean-deploy.md)
