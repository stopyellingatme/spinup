# Deploy to DigitalOcean

This guide shows how to put a spinup app on a DigitalOcean droplet with one command.
The command is `spinup deploy digital-ocean`.
The alias `spinup deploy do` works too.
The reference is `docs/commands/deploy-digital-ocean.md`.

## What the command does

The command creates the resources that are missing.
It makes a droplet and a firewall. It makes a DNS record when you ask for a domain.
It writes the environment to `spinup.toml` and makes the missing secrets.
It commits the new files and then runs `spinup deploy`.
If a step fails, run the command again. Each run continues where the last run stopped.

## Before you start

You need these tools on your machine: `ssh`, `ssh-keygen`, `sops`, `age-keygen`, `git`, and `docker`.
You need a spinup project with the deploy pack.
You need an SSH public key. Pass it with `--ssh-key`.
Commit your changes first, or pass `--allow-dirty`.

## Sign in

Run the first deploy in a terminal. The command opens your browser and asks you to sign in.
You can also sign in on its own:

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean login
Open this URL in a browser to sign in:
https://cloud.digitalocean.com/v1/oauth/authorize?...
Signed in to DigitalOcean as you@example.com (team My Team)
```

The sign-in needs a browser on the machine that runs spinup.
The browser sends the answer to a loopback address of this machine.
If the browser does not open, copy the URL from the output. Pass `--no-browser` to skip the opener.

On a machine with no browser, and in CI, use a personal access token.
Create the token at `https://cloud.digitalocean.com/account/api/tokens`.
Then export the token:

<!-- doctest: skip -->
```console
$ export DIGITALOCEAN_ACCESS_TOKEN=<your token>
$ spinup deploy digital-ocean status
```

spinup reads `DIGITALOCEAN_ACCESS_TOKEN` first and `DIGITALOCEAN_TOKEN` second.
spinup never stores these tokens.
The browser sign-in stores a token in `~/.config/spinup/credentials/digitalocean.json`.
The file has mode 0600. Set `SPINUP_CREDENTIALS_DIR` to use another directory.
spinup refreshes the token when it is about to expire.
To remove the stored token, run `spinup deploy digital-ocean logout`.
The command revokes the token at DigitalOcean and deletes the file.

## The first deploy

Run this command in your project:

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --recovery-recipient age1...
```

The command prints the plan and asks one question.
The question names the size, the region, and the price for each month.
Answer it, or pass `--yes`. With no terminal and no `--yes`, the command fails and names `--yes`.
Use `--plan` to see the plan and change nothing.

Without `--domain`, the app gets an address that ends in `.sslip.io`.
The address holds the IP address of the droplet.
Caddy gets a Let's Encrypt certificate for it at once.
All users of sslip.io share one Let's Encrypt limit, so use your own domain for a real app.

## A custom domain

Pass `--domain app.example.com`.
If DigitalOcean DNS holds the zone, spinup creates or updates the A record.
spinup finds the zone by the longest matching suffix.
If another provider holds the zone, spinup prints the record to create.
It then waits for the record to resolve. The wait lasts for `--dns-timeout` (10 minutes by default).
spinup warns you about an AAAA record that points to another host.
A change of the domain needs a new web image, so you cannot use `--skip-build` with it.

## Sizes, prices, and regions

The default region is `nyc3`. Change it with `--region`.
The default size for `production` is `s-2vcpu-4gb`. It costs about $24 each month.
The default size for other environments is `s-1vcpu-2gb`.
Change the size with `--size`.
spinup checks that the size has enough CPUs for `deploy/environments/<env>.toml` and at least 2 GiB of memory.
Only Ubuntu images work. The default is `ubuntu-24-04-x64`.

## The firewall

spinup makes a Cloud Firewall named `spinup-<app>-<env>`.
The firewall allows 80/tcp, 443/tcp, and 443/udp from all addresses.
It allows 22/tcp from all addresses unless you pass `--ssh-from`.
Pass a comma list, for example `--ssh-from 203.0.113.4/32`.
Docker bypasses the ufw rules on the droplet, so the Cloud Firewall is the real filter.

## Host key pins

spinup makes an ed25519 host key on your machine before it creates the droplet.
It writes the public key to `host_keys` in `spinup.toml`.
Cloud-init installs the key on the droplet, so the first connection is already trusted.
No trust on first use happens.
The private key stays readable in the droplet metadata, so spinup replaces the key at once.
It does this in two steps, and both keys are valid during the change.
A host key mismatch stops the command. spinup never retries it.

## Secrets

If `secrets/<env>.enc.yaml` does not exist, spinup creates it.
You must pass `--recovery-recipient <age recipient>` or `--no-recovery`.
Without one of these flags, the command stops before it creates anything, and it names the two flags.
spinup makes only the secrets that are missing.
It makes `POSTGRES_PASSWORD` and `DATABASE_URL`.
It makes `GRAFANA_ADMIN_PASSWORD` when monitoring is on.
spinup never replaces a value and never prints a value.

## The commit

The command commits `spinup.toml`, `.sops.yaml`, and `secrets/<env>.enc.yaml`.
The message is "spinup: provision <env> on DigitalOcean".
Pass `--no-commit` to stop before the deploy and commit the files yourself.

## Later deploys

After the first run, use plain `spinup deploy`.
It needs no DigitalOcean token, so your CI does not change.
If the deploy fails, run `spinup deploy --resume`.

## Status

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean status
Token source: store (access token expires 2026-10-22T09:00:00Z)
...
```

The command shows the token source, the account, and the droplet, the firewall, and the DNS record.
It also lists differences between `spinup.toml` and DigitalOcean.
Add the global `--json` flag for JSON output.

## Destroy

Run `spinup deploy digital-ocean destroy --i-know`.
Type the environment name when spinup asks.
The command deletes the droplet, the firewall, and the A record that points at the droplet.
It deletes only resources with the tag of the environment and the IDs in `spinup.toml`.
It keeps the account SSH key, the local secrets, and the age key.
The database on the droplet is lost, so run `spinup backup` first.

## Troubleshooting

- **cloud-init failed.** The command prints the errors and the line `Try: spinup deploy digital-ocean --recreate`. This deletes the droplet and creates a new one. If a release exists, add `--i-know`.
- **Host key mismatch.** Someone replaced the droplet, or something else answers at the address. Check the droplet in the DigitalOcean console. Then run `--recreate` if you need a new droplet.
- **HTTP 422 on create.** The droplet limit of the account is full. Delete a droplet or ask DigitalOcean to raise the limit.
- **HTTP 403.** The token lacks a scope. The message names the scope. Sign in again with `spinup deploy digital-ocean login`, or create a token with that scope.
- **HTTP 401.** The token expired or DigitalOcean revoked it. Sign in again.
- **Wrong team.** The token belongs to another team than the environment. Sign in to the team that owns the environment.
- **The sign-in is not available in this build.** A build without a registered client ID has no browser sign-in. Use a personal access token, or set `SPINUP_DO_CLIENT_ID`.

## Costs

A droplet costs money for each hour until you destroy it.
If a run fails after the droplet exists, the command prints the droplet, the price for each hour, and the destroy command.
DigitalOcean backups and monitoring are off.
