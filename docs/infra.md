# Hetzner Cloud infrastructure

The optional `tofu` module creates a Hetzner Cloud server, firewall, SSH key, and attached volume.
It requires the `deploy` module.
The stack manifest pins OpenTofu and the Hetzner Cloud provider.
The default image is `ubuntu-24.04`.

Preview the generated files:

```console
$ spinup new preview --api-only --modules deploy,tofu --module example.com/preview --no-git --non-interactive --dry-run
...
deploy/infra/tofu/main.tf (managed)
...
deploy/infra/tofu/versions.tf (managed)
...
```

Inspect the commands:

```console
$ spinup infra --help
...
  apply       Apply infrastructure changes and update the environment host
  destroy     Destroy the environment infrastructure
  output      Show infrastructure outputs
  plan        Preview infrastructure changes
...
```

Create an app with `spinup new <name> --modules deploy,tofu`.
For an existing app, run `spinup module add tofu`.
Install OpenTofu with `spinup tools install tofu`.
The installer checks the archive checksum before it installs the executable.
The commands find this managed executable without a change to `PATH`.

Copy `deploy/infra/tofu/terraform.tfvars.example` to `deploy/infra/tofu/terraform.tfvars`.
Set the server type, location, public key path, volume size, and resource prefix in that file.
Use an x86 server type with the default deployment images.
Use a unique resource prefix and SSH key for each environment.
The example file is a seed file, so upgrades preserve your changes.

The `.gitignore` excludes state, local variable files, plans, and `.terraform/`.
Keep `.terraform.lock.hcl` in version control.

Add the target environment to `spinup.toml` before provisioning:

```toml
[env.staging]
host = ""
user = "deploy"
public_url = "https://staging.example.com"
protected = false
```

Export `HCLOUD_TOKEN` in your shell.
Use a token with write access to the Hetzner Cloud project.
The provider reads the token from the environment.
Do not put the token in a file or a command argument.
Spinup redacts the token from subprocess output.

Run `spinup infra plan --env staging` to inspect the proposed resources.
Run `spinup infra apply --env staging` to create them.
With no terminal, pass `--yes` to approve the apply operation.

Spinup initializes OpenTofu with `-input=false` before each infrastructure operation.
Each environment uses `terraform.<name>.tfstate` and `.terraform/<name>/` within `deploy/infra/tofu/`.
The local backend uses the default workspace.

Preserve the state files and their backups outside version control.
Spinup refreshes provider and OpenTofu metadata during each operation.

Select the correct variable values before each operation in an environment.
Local variable files apply to every environment.
You can use `TF_VAR_server_type`, `TF_VAR_name_prefix`, and the other Terraform variable names instead.
Do not mix direct tofu commands with Spinup commands unless you select the same backend and data directory.

The server boots with `deploy/cloud-init/user-data.yaml` from the deploy pack.
The pack replaces the deploy key placeholder with the selected public key.
Cloud-init creates the deploy user, installs Docker, and prepares `/opt/<app>/`.
The cloud firewall permits TCP ports 22, 80, and 443, plus UDP port 443 for HTTP/3.

The volume uses ext4 and attaches with automount enabled.
The default Docker volumes remain on the server disk.
Configure volume use separately if application data must reside on the attached volume.

After a successful apply, Spinup reads the `ipv4` output.
It writes that address to `[env.<name>] host` in `spinup.toml`.
It preserves comments and unrelated settings.
If the apply or output fails, Spinup preserves the existing host.
Run `spinup infra output --env staging` to view `ipv4`, `ipv6`, and `server_id`.
Add `--json` for JSON output.

Point the application DNS record at the `ipv4` output.
DNS management is outside this pack.
Wait for cloud-init to finish before you deploy the app.

Run `spinup infra destroy --env staging --i-know` to remove the resources, including the volume and its data.
For a protected environment, type the environment name when prompted.
In CI, a protected environment also requires `--yes`.
Without a terminal, every destroy operation requires `--yes`.
The `--i-know` flag is always required for destruction.

The `--dry-run` flag runs a plan for apply or destroy.
A dry run does not change the host entry.
