---
title: Run the DigitalOcean live test
parent: Contributing
nav_order: 10
---

# Run the DigitalOcean live test

The script `scripts/do-live-test.sh` repeats the live test of `spinup deploy digital-ocean` on a real account.
The script is not part of `make` or CI, and no doctest runs it.
Use it before you change the DigitalOcean deploy code. The [Docker-in-Docker suite](dind.md) tests the same flow against a fake API.

<!-- doctest: skip -->
```sh
SPINUP_DO_LIVE=1 scripts/do-live-test.sh ~/code/todo live-test --ssh-key ~/.ssh/id_ed25519.pub
```

The rest of this page explains the cost, the prerequisites, and each check.

## Cost

The script creates one droplet, one firewall, and one tag, and it destroys them at the end.
The default size is `s-1vcpu-2gb` (`s-2vcpu-4gb` for the environment `production`). Pass `--size` to change it.
A `s-1vcpu-2gb` droplet costs a few cents for the short run.
If a step fails, the droplet can stay on the account and keep costing money.
The script then prints the destroy command. Run it.
The command looks like this:

```sh
spinup -C ~/code/todo --env live-test --confirm-env live-test deploy digital-ocean destroy --i-know
```

If the app declares sidecar services, two colors run during a color switch.
Use the size `s-2vcpu-4gb` for such an app.

## Prerequisites

- A DigitalOcean account that holds no other droplets, firewalls, or tags.
  The last step fails when anything but the SSH key is left.
- A stored sign-in with a refresh token. Run `spinup deploy digital-ocean login` first.
- A spinup project with the deploy pack and a clean git tree.
  An app that `spinup new` made is enough.
- For the full test, use an app with the `jobs` module, one `rust` service, and one `python` service.
  Add the services with `spinup g service <name> --lang rust|python`.
  Without services, the service step does nothing.
- The tools `jq`, `curl`, `openssl`, `ssh`, `docker`, `sops`, and `age-keygen`.
- A terminal. The script reads the answers from `/dev/tty`. Without a terminal, set `SPINUP_DO_LIVE_YES=1` to answer yes. Set it only after a person approved the cost.
- `--yes` in the provision flags and `SPINUP_DO_DEPLOY_ARGS=--yes`, because the provision and deploy runs have no terminal.

## Run the test

The command at the top of this page is the whole run.
The script refuses to run when `SPINUP_DO_LIVE` is not `1`.
It needs the app directory and the environment name.
Pass the provision flags after them.

The script prints the plan and asks for `yes` before it creates anything.
It asks again before it destroys the environment.

| Variable | Effect |
| --- | --- |
| `SPINUP_DO_LIVE=1` | Required. Without it, the script stops. |
| `SPINUP_BIN=<path>` | Test a spinup binary that is not in `PATH`. |
| `SPINUP_DO_DEPLOY_ARGS=<flags>` | Extra flags for the deploy runs, such as `--yes`. |
| `SPINUP_DO_WAIT_SECONDS=<seconds>` | The wait for each long step. The default is 600. |
| `SPINUP_CREDENTIALS_DIR=<dir>` | The directory of the stored sign-in. |
| `SPINUP_DO_LIVE_YES=1` | Answer yes to the questions. Use it only after a person approved the cost. |
| `SPINUP_DO_VERIFY_CMD=<command>` | Your own check after the service step. See [the service step](#step-7-services-and-the-hook). |
| `SPINUP_DO_PUBLIC_COOKIE=<cookie>` | The `Cookie` header of a signed-in session, for public routes with `auth = "session"`. |

## What the script checks

| Step | What happens | Pass when |
| --- | --- | --- |
| 1 | A provision run with `--skip-deploy` gets `kill -9` after the line `Created the droplet`. | The run stops while the droplet exists. |
| 2 | A second provision run. | It says `Using the droplet`. |
| 3 | A third run, with the deploy. | It creates and deletes nothing. |
| 4 | The script moves the expiry of the stored token into the past. | The next command refreshes the token and writes a later expiry. The script never prints a token value. |
| 5 | A plain `spinup deploy` with an empty `SPINUP_CREDENTIALS_DIR` and no `DIGITALOCEAN_*` variable. | The deploy works with no DigitalOcean token. |
| 6 | `curl` requests the public URL. | The certificate is valid. |
| 7 | The services and the hook (below). | Each service check passes. |
| 8 | `spinup deploy digital-ocean destroy --confirm-env <env> --i-know` deletes the environment. | The command succeeds. |
| 9 | The script lists the droplets, firewalls, tags, and SSH keys. | Only the SSH key is left. |

The script prints only the names of these resources, and no other data.

### Step 7: services and the hook

The script connects to the droplet over `ssh` with the host, the user, and the pinned host key of `spinup.toml`.
The private key is the `--ssh-key` public key without `.pub`. The default is `~/.ssh/id_ed25519`.
If `spinup.toml` declares `[[services]]`, the live color must pass these checks for each service:

- Each service container runs and is healthy, and the old color runs no such container.
- `GET <health>` answers through the `caddy` container.
- The `api` container and the `worker` container have `SERVICE_<NAME>_URL`.

For each service with `public` in `spinup.toml`, the script requests `https://<host><public><health>`:

| `auth` | Request | Expected answer |
| --- | --- | --- |
| `none` | no cookie | HTTP 200 |
| `session` | no cookie | HTTP 401 |
| `session` | the cookie in `SPINUP_DO_PUBLIC_COOKIE` | HTTP 200 |

If no service is public, the script skips these checks.

If the app has the `jobs` module, the `worker` container must run in the live color only.
It must not restart during a wait of 10 seconds.

If `SPINUP_DO_VERIFY_CMD` is set, the script runs it with `bash -c`.
The script exports `SPINUP_DO_HOST` (the droplet IP) and `SPINUP_DO_SSH_KEY` (the private key file).
If the command fails, the script stops and prints the destroy command.

A successful run ends with the list of what is left and this line:

```text
== 9. Resources left on the account (names only)
droplets: none
firewalls: none
tags: none
ssh keys: my-laptop

The live test passed. Only the SSH key is left.
```

## See also

- [Docker-in-Docker deploy suite](dind.md), the same flow against a fake API
- [DigitalOcean OAuth client](digitalocean-client.md)
- [Deploy to DigitalOcean](../deploy-digitalocean.md)
