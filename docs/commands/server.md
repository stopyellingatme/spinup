# Server commands

The `server` commands prepare and inspect a Debian or Ubuntu VM through SSH.
On Windows, run these commands inside WSL2.
Configure `host`, `user`, and `port` under `[env.<name>]` in `spinup.toml`.
The commands use your SSH configuration and require no interactive prompts.
Use `--accept-new-host-key` to accept a previously unknown host key.
Changed host keys still fail verification.

```console
$ spinup server --help
...
$ spinup server bootstrap --help
...
$ spinup server audit --help
...
$ spinup server logs --help
...
$ spinup server ssh --help
...
```

Connect as a user with passwordless sudo for bootstrap and the SSH audit.
The deploy user needs Docker access for audits and logs.
The Docker group grants access equivalent to root.

<!-- doctest: skip -->
```console
$ spinup server bootstrap --env staging --ssh-key ~/.ssh/id_ed25519.pub --plan
...
$ spinup server bootstrap --env staging --ssh-key ~/.ssh/id_ed25519.pub
...
$ spinup server bootstrap --env staging --ssh-key ~/.ssh/id_ed25519.pub --print-cloud-init > user-data.yaml
```

The public key defaults to `~/.ssh/id_ed25519.pub`.
Bootstrap checks each step before it changes the host.
A second bootstrap reports `ok` for steps that already meet their requirements.
Bootstrap also installs `/etc/sudoers.d/spinup-<app>` with mode `0440`.
It validates the staged file with `visudo -cf` before moving it into place.
Cloud-init uses the same policy and validation.
The policy permits backup units to use specific systemctl commands and fixed install paths.
It does not grant a general shell through sudo.

The `--plan` flag prints `would-change` or `ok` for each step.
The global `--dry-run` flag also selects plan mode.
Plan mode requires SSH and sudo access but changes no host settings.

Bootstrap installs the deploy user, the operator key, and the SSH settings.
It disables root login, password authentication, and keyboard-interactive authentication.
It validates the SSH configuration before reload and restores the previous files if validation fails.

Bootstrap permits every SSH port reported by `sshd -T` and the current SSH connection port through UFW.
This includes the connection port selected by `spinup.toml`, `~/.ssh/config`, or `SPINUP_SSH_CONFIG`.
It also permits TCP ports 80 and 443, and UDP port 443.
Bootstrap stops before enabling UFW if it cannot determine valid SSH ports.
It enables fail2ban and automatic security upgrades.

Bootstrap installs Docker from its official apt repository with a verified signing key.
It installs the Compose plugin and adds deploy to the Docker group.
It preserves existing Docker settings when it configures log rotation.
The log limits are `max-size: 10m` and `max-file: 3`.
A change to the log settings restarts Docker.

Bootstrap creates a swapfile sized to RAM unless a swapfile or active swap already exists.
It configures networking settings and creates `/opt/<app>/{releases,secrets,backups,data}`.
It also creates the stable Caddy and Postgres directories required by the deploy pack.
The deploy user owns these directories.
The secrets directory has mode `0700`.

The `--print-cloud-init` flag reads the rendered `deploy/cloud-init/user-data.yaml` from the app.
It replaces the SSH key placeholder and prints the result without connecting to the VM.
The cloud-init file retains the settings from the deploy pack, including its fixed swap size.

<!-- doctest: skip -->
```console
$ spinup server audit --env staging
...
$ spinup server audit --env staging --json
...
$ spinup server logs --env staging api --follow --since 10m
...
$ spinup server ssh --env staging
...
```

Audit checks every running container on the host, including containers outside the app.
It checks SSH authentication, root login, container restrictions, environment secrets, disk space, and unattended upgrades.
It rejects every published Postgres port.
It permits public ports only for the Caddy service on ports 80 and 443.
All other published ports must bind to `127.0.0.1`.
Docker can bypass UFW, so the audit checks Docker bindings directly.

Each failed check includes a `Try:` line.
Audit exits with status 1 if any check fails or cannot read its required settings.
The `--json` flag prints an array with check names, statuses, messages, and corrective actions.
The report never includes inspected environment values.
The disk check requires more than 15 percent free space.

Logs include all running replicas in the base project and both color projects of the app.
The `--follow` flag streams their logs until interrupted.
The `--since` flag accepts Docker durations or timestamps.
The `ssh` command opens an interactive login shell with the configured user.

Bootstrap prints RAM recommendations to stderr after a successful probe or bootstrap.
The remote doctor check `vm-memory` needs `Context.Deps["remote"]`.
It recommends the `lite` monitoring profile below 4 GiB of RAM.
It warns below 2 GiB of RAM.
