# Deploy to DigitalOcean

`spinup deploy digital-ocean` takes an app from no server to a live deploy on DigitalOcean.
The aliases are `digitalocean` and `do`.
The user guide is `docs/deploy-digitalocean.md`. The contract is `docs/adr/0017-digitalocean-deploy.md`.

The command signs in, creates a droplet, a firewall, and a DNS record, makes the secrets, commits the new files, and runs the normal `spinup deploy`.
A second run continues after a failure.
After the first run, plain `spinup deploy` needs no DigitalOcean token.

<!-- doctest: skip -->
```console
$ spinup deploy digital-ocean --region nyc3 --no-recovery --yes
Region: nyc3
Droplet: create app-production, size s-2vcpu-4gb ...
...
Deployed release 20261022T080000Z-0123456789ab to production (green color)
```

The command refuses `--offline`:

```console
$ spinup deploy digital-ocean status --offline
Error: Use DigitalOcean
  the command needs the network, and --offline is set
  Try: run the command without --offline
[exit 2]
```

## Flags

| Flag | Meaning |
|---|---|
| `--region`, `--size`, `--image` | The droplet. The image must be an Ubuntu image. |
| `--domain`, `--public-url` | The host name and the public URL. The default is an sslip.io address. |
| `--ssh-key`, `--ssh-from` | The operator public key file, and the sources that may reach port 22. |
| `--no-browser` | Print the sign-in URL and do not open a browser. |
| `--recovery-recipient`, `--no-recovery` | The answer to the recovery question of `spinup secrets init`. |
| `--plan` (or `--dry-run`) | Print what would change and change nothing. |
| `--skip-deploy`, `--no-commit` | Stop before the deploy, or do not commit the new files. |
| `--recreate`, `--i-know` | Delete the droplet and create a new one. `--i-know` accepts the loss of data. |
| `--dns-timeout` | The wait for an external DNS record (default 10m). |
| `--allow-dirty`, `--allow-destructive`, `--skip-build`, `--tag`, `--platform`, `--health-timeout`, `--no-auto-rollback` | The same flags as `spinup deploy`. |

Before it creates a droplet, the command asks for the price once.
`--yes` answers the question. With no terminal and no `--yes`, the command fails and names `--yes`.

## Subcommands

### spinup deploy digital-ocean login

Sign in with a browser and store the token.
The command always runs a new sign-in, even when a token is stored.
It needs a browser on this machine, because the answer comes back to a loopback address.
With `--no-browser`, the command prints the URL and you open it yourself.
It prints the account and the team, never the token.

### spinup deploy digital-ocean logout

Revoke the stored token and delete the token file.

### spinup deploy digital-ocean status

Show where the token comes from, the account, and the resources of the environment.
Outside a project, it shows the account only.
Use the global `--json` flag for JSON output.

### spinup deploy digital-ocean destroy

Delete the droplet, the firewall, and the A record of the environment.
The command needs `--i-know`.
The database on the droplet is lost, so run `spinup backup` first.

```console
$ spinup deploy digital-ocean destroy
Error: destroy the environment
  the command deletes the droplet and its database
  Try: pass --i-know
[exit 1]
```
