# spinup deploy digital-ocean login

Sign in to DigitalOcean in a browser and store the token

Open the DigitalOcean sign-in page, wait for the answer on a loopback address of this machine,
and store the token. The command needs a browser on this machine.
Use a personal access token in DIGITALOCEAN_ACCESS_TOKEN on a machine with no browser.

## Use

```text
spinup deploy digital-ocean login [flags]
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--accept-new-host-key` | `false` | accept the SSH host key of a new VM |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-browser` | `false` | print the sign-in URL and do not open a browser |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |
