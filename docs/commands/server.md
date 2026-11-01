---
title: server — prepare and inspect a VM
parent: Command guides
---

# server — prepare and inspect a VM

`spinup server` prepares a fresh Debian or Ubuntu VM for your app, checks that it stays safe, and lets you read its logs or open a shell.
Run `bootstrap` once before the first [deploy](deploy.md).

The example below prints the cloud-init file for the VM, so it needs no server:

```console
$ spinup new todo --api-only --modules deploy --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ printf '\n[env.staging]\nhost = "203.0.113.10"\nuser = "deploy"\n' >> spinup.toml
$ ssh-keygen -q -t ed25519 -N '' -f key
$ spinup server bootstrap --env staging --ssh-key key.pub --print-cloud-init | head -1
#cloud-config
```

The commands use SSH. Set `host`, `user`, and `port` under `[env.<name>]` in `spinup.toml`.
Your `~/.ssh/config` also applies.
On Windows, run the commands inside WSL2.

## Prepare a VM

`bootstrap` connects as a user with passwordless sudo and makes the VM ready for deploys.
It checks each step first, so you can run it again. A step that is already right reports `ok`.

<!-- doctest: skip -->
```console
$ spinup server bootstrap --env staging
ok        packages
changed   deploy-user
changed   sshd
...
```

The public key of the operator defaults to `~/.ssh/id_ed25519.pub`. Pass `--ssh-key` to use another file.

<!-- doctest: skip -->
```console
$ spinup server bootstrap --env staging --ssh-key ~/.ssh/work.pub
```

If the SSH host key is new, add `--accept-new-host-key`. A changed host key always fails.

### What bootstrap sets up

| Step | Result |
| --- | --- |
| `packages` | The base packages. |
| `deploy-user` | The `deploy` user with your public key, in the `docker` group. |
| `backup-sudo` | The file `/etc/sudoers.d/spinup-<app>` (mode `0440`, checked with `visudo -cf`). It lets backup units run fixed `systemctl` and install commands, with no general shell. |
| `sshd` | Root login, password login, and keyboard-interactive login off. The old files come back if the new settings fail the check. |
| `firewall` | UFW allows every SSH port from `sshd -T`, the port of the current connection, TCP 80 and 443, and UDP 443. Bootstrap stops before it enables UFW if it cannot find a valid SSH port. |
| `fail2ban` | Blocks repeated failed sign-ins. |
| `unattended-upgrades` | Security updates install on their own. |
| `docker-repository`, `docker` | Docker and the Compose plugin from the official apt repository. |
| `docker-logs` | Log rotation: 10 MB for each file, 3 files. Other Docker settings stay. A change restarts Docker. |
| `swap` | A swap file the size of the RAM, unless the VM has swap already. |
| `sysctl` | Low swappiness and larger network queues. |
| `layout` | `/opt/<app>/{releases,secrets,backups,data}` plus the Caddy and PostgreSQL directories. The `deploy` user owns them, and `secrets` has mode `0700`. |

{: .note }
The `deploy` user is in the `docker` group. That access equals root on the VM.

### Preview with --plan

`--plan` connects, checks, and prints `would-change` or `ok` for each step. It changes nothing.
It still needs SSH and sudo. The global `--dry-run` flag does the same.

<!-- doctest: skip -->
```console
$ spinup server bootstrap --env staging --plan
would-change   deploy-user
ok             packages
...
```

### Use cloud-init instead

If your provider accepts a user-data file, skip SSH. `--print-cloud-init` prints the rendered `deploy/cloud-init/user-data.yaml` with your public key filled in.
It makes the same setup as `bootstrap`, with a fixed swap size.

<!-- doctest: skip -->
```console
$ spinup server bootstrap --env staging --print-cloud-init > user-data.yaml
```

You cannot combine `--plan` and `--print-cloud-init`.

After a successful bootstrap, spinup prints a RAM tip.
Below 4 GiB, it recommends the `lite` monitoring profile. Below 2 GiB, it warns.

## Audit the VM

`audit` reports unsafe settings on the VM. It reads the host and changes nothing.

<!-- doctest: skip -->
```console
$ spinup server audit --env staging
ok      ssh-keys-only        SSH must permit only public key authentication.
ok      ssh-root-login       SSH root login must be disabled.
fail    public-port:grafana  Only Caddy ports 80 and 443 may be public.
Try: Bind published ports to 127.0.0.1. Publish only Caddy ports 80 and 443.
...
```

Each failed check prints a `Try:` line, and the command exits with status 1.
Add the global `--json` flag for an array of check names, statuses, messages, and fixes.
The report never contains the values it inspected.

The audit checks:

- SSH accepts public keys only, and root login is off.
- Disk space is more than 15 percent free.
- Unattended upgrades are active.
- Every running container, including containers outside your app: no published PostgreSQL port, no public port except Caddy on 80 and 443, no literal secrets in the environment, and container limits such as non-root, read-only, and dropped capabilities.

Docker can bypass UFW, so the audit checks the Docker port bindings directly.

<!-- doctest: skip -->
```console
$ spinup server audit --env staging --json
[{"check":"ssh-keys-only","status":"ok","message":"SSH must permit only public key authentication."},...]
```

## Read logs

`logs` streams the logs of one service from every running replica, in the base project and in both colors.

<!-- doctest: skip -->
```console
$ spinup server logs api --env staging --since 10m
$ spinup server logs api --env staging --follow
```

`--since` takes a duration such as `10m` or a timestamp. `--follow` keeps streaming until you press Ctrl+C.

## Open a shell

`ssh` opens an interactive shell on the VM as the configured user.

<!-- doctest: skip -->
```console
$ spinup server ssh --env staging
```

## Requirements

- Bootstrap and the SSH part of the audit need a user with passwordless sudo.
- The audit and the logs need Docker access for the deploy user.
- The commands never prompt. Pass `--accept-new-host-key` for a host key that you trust.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `read operator SSH key` | The key file is missing. Pass `--ssh-key <file>`. |
| Host key error | The VM is new or its key changed. For a new VM, add `--accept-new-host-key`. |
| `bootstrap` stops before UFW | spinup could not find an SSH port. Check `sshd -T` on the VM. |

## See also

- [Deploy to your own server](../deploy.md) and [Deploy to DigitalOcean](../deploy-digitalocean.md)
- [deploy](deploy.md) and [secrets](secrets.md)
- [Monitoring](../monitoring.md): the `lite` and `full` profiles
- Flag reference: [server](../site/commands/server.md), [bootstrap](../site/commands/server-bootstrap.md), [audit](../site/commands/server-audit.md), [logs](../site/commands/server-logs.md), [ssh](../site/commands/server-ssh.md)
