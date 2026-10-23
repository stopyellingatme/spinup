# spinup deploy digital-ocean

Create a DigitalOcean droplet for the app and deploy to it

Sign in to DigitalOcean, create a droplet with a firewall and a DNS record, make the secrets,
commit the new files, and run the normal deploy. A second run continues after a failure.
After the first run, plain spinup deploy needs no DigitalOcean token.

## Use

```text
spinup deploy digital-ocean [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--accept-new-host-key` | `false` | accept the SSH host key of a new VM |
| `--allow-destructive` | `false` | deploy migrations that db lint reports as unsafe |
| `--allow-dirty` | `false` | deploy with uncommitted changes |
| `-C, --dir` | `` | run in this project directory |
| `--dns-timeout` | `10m0s` | time to wait for a DNS record that DigitalOcean does not hold |
| `--domain` | `` | host name of the app (DigitalOcean DNS, or an external record that you create) |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--health-timeout` | `2m0s` | time for the new color to become healthy |
| `--i-know` | `false` | accept the loss of data when --recreate deletes a droplet with a release |
| `--image` | `` | droplet image slug (Ubuntu only) |
| `--json` | `false` | write JSON output |
| `--no-auto-rollback` | `false` | leave an unhealthy new color running for debugging |
| `--no-browser` | `false` | print the sign-in URL and do not open a browser |
| `--no-color` | `false` | turn off color |
| `--no-commit` | `false` | do not commit the provisioning files |
| `--no-recovery` | `false` | make the secrets with no recovery recipient |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--plan` | `false` | print what would change and change nothing |
| `--platform` | `` | image platform (default linux/amd64) |
| `--public-url` | `` | public URL of the app (default from the domain, else an sslip.io address) |
| `--recovery-recipient` | `` | age recipient that can recover the secrets |
| `--recreate` | `false` | delete the droplet and create a new one |
| `--region` | `` | DigitalOcean region slug (default from the stored value, then nyc3) |
| `--size` | `` | droplet size slug (default s-2vcpu-4gb for production, else s-1vcpu-2gb) |
| `--skip-build` | `false` | use the images of the release that exist already |
| `--skip-deploy` | `false` | stop before the deploy |
| `--ssh-from` | `` | comma list of sources that may reach port 22 (default all) |
| `--ssh-key` | `` | operator SSH public key file |
| `--tag` | `` | release ID of images that exist already (<YYYYMMDDTHHMMSSZ>-<12-char SHA>) |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Subcommands

- [`spinup deploy digital-ocean destroy`](deploy-digital-ocean-destroy.md) — Delete the droplet, the firewall, and the DNS record of the environment
- [`spinup deploy digital-ocean login`](deploy-digital-ocean-login.md) — Sign in to DigitalOcean in a browser and store the token
- [`spinup deploy digital-ocean logout`](deploy-digital-ocean-logout.md) — Revoke the stored DigitalOcean token and delete it
- [`spinup deploy digital-ocean status`](deploy-digital-ocean-status.md) — Show the token source, the account, and the resources of the environment
