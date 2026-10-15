# Secrets

`spinup secrets` manages encrypted secrets for each environment with sops and age.
Install `sops` and `age-keygen` before you use these commands.
Select the environment with `--env`.
Select another project with `--dir`.

```console
$ spinup secrets init
Error: select secrets environment: --env is missing or invalid
  Try: pass --env <environment>
[exit 1]
$ spinup secrets edit --env staging
Error: edit secrets: a terminal is required
  Try: run in a terminal or use spinup secrets set --env <environment> <NAME>
[exit 1]
```

Create a separate recovery key outside the repository.
Store its private key in a safe location.
Use its public recipient when you initialize each environment.
Use a separate recipient for CI.

<!-- doctest: skip -->
```console
$ age-keygen -o /safe/location/recovery.agekey
$ spinup secrets init --env staging --recovery-recipient age1... --ci-recipient age1...
```

`init` creates `secrets/staging.enc.yaml` and updates `.sops.yaml` without removing comments.
Each creation rule includes the environment recipient and the supplied recovery and CI recipients.
The environment key is `~/.config/spinup/keys/<app>-<env>.agekey`.
The directory mode is 0700, and the key mode is 0600.
`SPINUP_KEYS_DIR` changes the directory for keys.

`init` requires a recovery recipient.
`--no-recovery` permits initialization without recovery and prints a warning.
Loss of all recipient keys permanently prevents decryption.
Never commit `*.agekey` or `.env` files.
Add `*.agekey` and `.env` to `.gitignore`.

Read secret values from stdin with `set`.
The command does not accept a value argument.
`set` preserves the input, including trailing newlines.
`get` writes the exact value to stdout without a status message or an added newline.

<!-- doctest: skip -->
```console
$ printf '%s' 'postgres://user:password@postgres/app' | spinup secrets set --env staging DATABASE_URL
$ spinup secrets get --env staging DATABASE_URL
$ spinup secrets edit --env staging
$ spinup secrets diff --env staging
added DATABASE_URL
```

`edit` requires a terminal and uses the configured sops editor.
Secret names must use uppercase environment variable syntax, such as `DATABASE_URL`.
The encrypted document must contain one flat map with string values.
`diff` compares decrypted entries against Git HEAD and prints only changed names.
An environment without a committed encrypted file compares against an empty map.
The names use sorted order with the labels `added`, `removed`, and `changed`.

Use `rotate` to replace the data encryption key.
After you change recipients in `.sops.yaml`, use `rekey` to update the encrypted file.
Both commands require confirmation for a protected environment.
In CI, pass `--yes` to confirm the change.

<!-- doctest: skip -->
```console
$ spinup secrets rotate --env staging
$ spinup secrets rekey --env staging
```

Deployment decrypts secrets locally and uploads one file for each name to `/opt/<app>/secrets/`.
Each file uses mode 0600.
The synchronization removes secret files whose names are absent from the current map.
The deployer must hold the deployment lock during synchronization.
Release snapshots must contain only hashes of secrets.

The kit package `secrets` provides `Load(name)` and `Must(name)`.
`Load` reads `<NAME>_FILE` before `<NAME>` and removes one trailing newline.
An unreadable file returns an error even when `<NAME>` has a value.
`Must` panics when `Load` returns an error.
Neither function logs a value.

The full `doctor` command includes the `plaintext-secrets` check.
It rejects tracked `.env` files, tracked `*.agekey` files, and tracked YAML files under `secrets/` without sops metadata.
It also rejects a `.gitignore` that does not exclude `.env`.
The quick doctor check skips this check.

## Push secrets to the host

`spinup deploy` writes the decrypted secrets to `/opt/<app>/secrets/` on every deploy.
To change a value without a deploy, use `push`. It takes the deploy lock, writes one file per
secret with mode 0600, and removes files for names that no longer exist. Running containers read
the files when they start, so the next deploy or restart picks up the new values.

<!-- doctest: skip -->
```console
$ spinup secrets push --env staging
Pushed 3 secrets to staging: DATABASE_URL, DIND_TOKEN, POSTGRES_PASSWORD
Next: spinup deploy --env staging (running containers read the files at start)
```

A protected environment asks you to type its name first (or needs `--yes` in CI).
