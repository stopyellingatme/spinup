# spinup deploy

Ship the current commit to an environment with zero downtime

Build the images, ship them to the VM of the environment over SSH, run the migrations,
and switch Caddy to the new blue/green color. Each step is journaled on the VM.

## Use

```text
spinup deploy [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--accept-new-host-key` | `false` | accept the SSH host key of a new VM |
| `--allow-destructive` | `false` | deploy migrations that db lint reports as unsafe |
| `--allow-dirty` | `false` | deploy with uncommitted changes |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--health-timeout` | `2m0s` | time for the new color to become healthy |
| `--json` | `false` | write JSON output |
| `--no-auto-rollback` | `false` | leave an unhealthy new color running for debugging |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--plan` | `false` | print what would change and change nothing on the VM |
| `--platform` | `` | image platform (default linux/amd64) |
| `--redeploy` | `false` | reuse a release ID in a new directory with a -r<N> suffix |
| `--resume` | `false` | continue the last deploy from its first step that is not done |
| `--skip-backup` | `false` | skip the pre-deploy backup |
| `--skip-build` | `false` | use the images of the release that exist already |
| `--tag` | `` | release ID of images that exist already (<YYYYMMDDTHHMMSSZ>-<12-char SHA>) |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Subcommands

- [`spinup deploy digital-ocean`](deploy-digital-ocean.md) — Create a DigitalOcean droplet for the app and deploy to it
- [`spinup deploy status`](deploy-status.md) — Show the current release, the deploy lock, and the last deploy journal
- [`spinup deploy unlock`](deploy-unlock.md) — Remove the deploy lock of an environment
