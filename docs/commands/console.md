---
title: console — open the operator overview
parent: Command guides
---

# console — open the operator overview

`spinup console` groups the console commands. `spinup console open` opens the deployed overview through SSH.
The app must select the `console` module and have one completed console installation.
See [Operator console](../console.md) for adoption, disabled tiles, limits, and observation times.

```console
$ spinup console --help
...
$ spinup console open --help
...
```

## Open the page

<!-- doctest: skip -->
```console
$ spinup console open --env production
Console: http://127.0.0.1:49152/
Press Ctrl-C to close the tunnels.
```

The local port varies. The command verifies console health before it prints the URL or starts the browser.
With active monitoring, it also verifies a Grafana tunnel. The URL then includes `?grafana_port=<local-port>`.
A failed Grafana tunnel leaves the console available without Grafana links.
Keep the command running while you use the page. Press Ctrl-C to close only its tunnels.

Use `--no-browser` to print the verified URL without starting a browser.
If browser startup fails, open the printed URL manually. The command keeps the tunnels available.

The command requests a host sample when the previous sample exceeds 30 seconds or is missing.
It repeats that check every 30 seconds while open. This supplies a fallback when the systemd user timer cannot run.
Stale tiles retain their observation times. A source outage does not trigger direct terminal queries.

## Flags

| Flag | Meaning | Example |
| --- | --- | --- |
| `--no-browser` | Print the URL without starting a browser. | `spinup console open --env production --no-browser` |
| `--accept-new-host-key` | Accept a previously unknown SSH host key. | `spinup console open --env production --accept-new-host-key` |
| `--env <name>` | Select the deployed environment. | `--env production` |
| `--dir <path>`, `-C` | Select the project directory. | `-C ./todo` |

Before accepting a new host key, compare its fingerprint with your provider's record.
If the project has one environment or a default, you can omit `--env`.
Pinned host keys retain their existing SSH checks. See [Global flags](../reference/global-flags.md) for project and output flags.

## Diagnostics

| Message | Action |
| --- | --- |
| `Cannot open console: module console is disabled.` | Run `spinup module add console`, merge its branch, and deploy. |
| `Cannot read console: the service for <env> did not answer.` | Run `spinup doctor --remote --env <env>`. |
| `Console sample delayed` | The page keeps the previous sample. Check its observation times and run `spinup status --env <env>`. |

The console uses VM loopback port 7410. It has no public route or app login.
See [status](status.md) for the same overview in your terminal.
