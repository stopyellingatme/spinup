---
title: Hetzner Cloud with OpenTofu
parent: Deploy
nav_order: 5
---

# Hetzner Cloud with OpenTofu

The optional `tofu` module creates a Hetzner Cloud server, a firewall, an SSH key, and an attached volume.
Hetzner Cloud is the only provider it supports. For DigitalOcean, read [Deploy to DigitalOcean](deploy-digitalocean.md).
For a server you already have, read [Deploy to your own server](deploy.md).

<!-- doctest: skip -->
```console
$ spinup module add tofu
$ spinup tools install tofu
$ export HCLOUD_TOKEN=<your token>
$ spinup infra apply --env staging
```

The apply writes the server address into `spinup.toml`. Then you run `spinup deploy --env staging`.

## Quick start

1. **Add the module.** For a new app, run `spinup new todo --modules deploy,tofu`. For an existing app, run `spinup module add tofu`. The module needs the `deploy` module.
2. **Install OpenTofu.** The installer checks the archive checksum. The commands find the installed program without a change to `PATH`.

   ```sh
   spinup tools install tofu
   ```

3. **Set the variables.** Copy the example file and edit it:

   ```sh
   cp deploy/infra/tofu/terraform.tfvars.example deploy/infra/tofu/terraform.tfvars
   ```

4. **Add the environment** to `spinup.toml`. Leave `host` empty. The apply fills it in:

   ```toml
   [env.staging]
   host = ""
   user = "deploy"
   public_url = "https://staging.example.com"
   protected = false
   ```

5. **Export your Hetzner token.** Use a token with write access to the Hetzner Cloud project:

   ```sh
   export HCLOUD_TOKEN=<your token>
   ```

   The provider reads the token from the environment. Never put it in a file or a command argument. spinup hides it in subprocess output.
6. **Plan, then apply:**

   <!-- doctest: skip -->
   ```console
   $ spinup infra plan --env staging
   $ spinup infra apply --env staging
   ```

   In a terminal, OpenTofu shows the plan and asks you to type `yes`. Without a terminal, pass `--yes` to approve the apply.

## Configure the server

`terraform.tfvars` holds these variables:

| Variable | Default | Meaning |
| --- | --- | --- |
| `server_type` | `cx23` | The Hetzner server type. Use an x86 type, because the default images are x86. |
| `location` | `fsn1` | The data center. |
| `image` | `ubuntu-24.04` | The operating system image. |
| `ssh_public_key_path` | `~/.ssh/id_ed25519.pub` | The public key of the deploy user. |
| `volume_size` | `20` | The size of the attached volume in GiB. Use a whole number of 10 or more. |
| `name_prefix` | your app name | The prefix of every resource name. |

```
server_type         = "cx23"
location            = "fsn1"
ssh_public_key_path = "~/.ssh/id_ed25519.pub"
volume_size         = 20
name_prefix         = "todo-staging"
```

Use a unique `name_prefix` and SSH key for each environment.
You can also set a variable with `TF_VAR_<name>`, for example `TF_VAR_name_prefix`.
The file is a seed file, so upgrades keep your changes.

A variable file applies to every environment.
Change the values before you run a command for another environment.

The server boots with `deploy/cloud-init/user-data.yaml`.
The module puts your public key into it, and cloud-init creates the deploy user, installs Docker, and prepares `/opt/<app>/`.
The cloud firewall allows TCP 22, 80, and 443, and UDP 443 for HTTP/3.

The volume uses ext4 and mounts automatically.
Docker volumes stay on the server disk. Configure the volume yourself if app data must live on it.

## See the outputs

<!-- doctest: skip -->
```console
$ spinup infra output --env staging
ipv4       203.0.113.10
ipv6       2001:db8::1
server_id  12345678
$ spinup infra output --env staging --json
```

After a successful apply, spinup writes `ipv4` to `[env.<name>] host` in `spinup.toml` and keeps your comments.
If the apply or the output fails, the old host stays.

## Point DNS and deploy

1. Point the A record of your domain at the `ipv4` output. The module does not manage DNS.
2. Wait until cloud-init finishes on the server.
3. Create the secrets and deploy. See [Deploy to your own server](deploy.md).

## Where the state is stored

spinup runs OpenTofu with the local backend and `-input=false`.
Each environment keeps its state in `deploy/infra/tofu/terraform.<name>.tfstate` and its data in `.terraform/<name>/`.
The `.gitignore` already excludes the state, local variable files, plans, and `.terraform/`.
Commit `.terraform.lock.hcl`.

{: .warning }
The state files describe your servers. Back them up outside git. If you lose them, OpenTofu forgets what it created.

## Preview without a change

`--dry-run` runs a plan for `apply` or `destroy` and changes nothing, not even the host entry:

<!-- doctest: skip -->
```console
$ spinup infra apply --env staging --dry-run
```

To see the files that the `tofu` module adds to a new app:

```console
$ spinup new preview --api-only --modules deploy,tofu --module example.com/preview --no-git --non-interactive --dry-run
...
deploy/infra/tofu/main.tf (managed)
...
deploy/infra/tofu/versions.tf (managed)
...
```

## Destroy

{: .warning }
`destroy` removes the server and the volume with its data. Run `spinup backup` first.

<!-- doctest: skip -->
```console
$ spinup infra destroy --env staging --i-know
```

`--i-know` is always required.
For a protected environment, type the environment name when asked.
Without a terminal, pass `--confirm-env staging` and `--yes`:

```sh
spinup infra destroy --env production --i-know --confirm-env production --yes
```

In CI, `CI=true` with `--yes` also works.

## Use tofu directly

You can run `tofu` in `deploy/infra/tofu/` yourself.
Use the same backend and data folder as spinup, which means the `terraform.<name>.tfstate` file and `.terraform/<name>/` of your environment.
Otherwise tofu sees an empty state and tries to create a second server.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `HCLOUD_TOKEN` error | The token is not exported. | Export it in the same shell. |
| `tofu` not found | OpenTofu is not installed. | Run `spinup tools install tofu`. |
| The server type is not available | The type does not exist in that location, or it is not x86. | Pick another `server_type` or `location`. |
| The deploy cannot connect right after apply | cloud-init still runs. | Wait a few minutes and try again. |

## See also

- [Deploy overview](deploy/index.md)
- [Environments](deploy/environments.md)
- [The `infra` command guide](commands/infra.md)
- [Flag reference of `spinup infra`](site/commands/infra.md)
