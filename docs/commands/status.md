---
title: status — read the operator overview
parent: Command guides
---

# status — read the operator overview

`spinup status` reads the deployed console overview through authenticated SSH.
It requires the `console` module. See [Operator console](../console.md) for selection, deployment, limits, and disabled states.

```console
$ spinup status --help
...
```

## Read the overview

<!-- doctest: skip -->
```console
$ spinup status --env production
todo (production)
Observed: 2026-11-22T18:00:00Z
Live release
...
$ spinup status --env production --json
...
```

Text starts with the app, environment, and observation time. An unknown time prints `Observed: unknown`.
The nine sections follow in overview order, with indented fields or compact JSON records.
An empty or null success prints `none`. Disabled and unavailable sections have no data rows.
Stale sections retain their data and show `[stale: sample_expired]` beside the caption.

`--json` prints only the received document and one final newline after validation.
The command validates the version, identity, and shape before it writes stdout.
A disabled module, failed tunnel, or incompatible response causes a nonzero exit without partial JSON.
A valid partial overview exits 0, including unavailable or stale sections. Check each section's state when you automate decisions.

The command requests a sample through SSH when it exceeds 30 seconds or is missing.
This supplies a fallback when the systemd user timer cannot run. It then reads the same endpoint as the page.
Source outages remain visible in their sections. The terminal never queries the sources directly.

## Flags

| Flag | Meaning | Example |
| --- | --- | --- |
| `--json` | Print the validated overview document. | `spinup status --env production --json` |
| `--accept-new-host-key` | Accept a previously unknown SSH host key. | `spinup status --env production --accept-new-host-key` |
| `--env <name>` | Select the deployed environment. | `--env production` |
| `--dir <path>`, `-C` | Select the project directory. | `-C ./todo` |

Before accepting a new host key, compare its fingerprint with your provider's record.
If the project has one environment or a default, you can omit `--env`.
Pinned host keys retain their existing SSH checks. See [Global flags](../reference/global-flags.md).

## Diagnostics

| Message | Action |
| --- | --- |
| `Cannot open console: module console is disabled.` | Run `spinup module add console`, merge its branch, and deploy. |
| `Cannot read console: the service for <env> did not answer.` | Run `spinup doctor --remote --env <env>`. |
| `Cannot read console: the overview has an incompatible version, identity, or shape.` | Upgrade the CLI and deploy the console for that environment. |

`spinup deploy status` reads the deploy journal and lock. `spinup status` reads the wider operational overview.
See [console](console.md) to open the browser page.
