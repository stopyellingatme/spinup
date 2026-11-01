---
title: secrets — keep secrets encrypted
parent: Command guides
---

# secrets — keep secrets encrypted

`spinup secrets` stores the secrets of each environment in your repository as an encrypted file.
Only people and machines that hold a matching key can read it.
Use it for database passwords, API tokens, and anything else that must not appear in git.

Install `sops` and `age-keygen` first. The commands use them.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup secrets init --env staging --no-recovery
...
$ printf '%s' 'postgres://app:pw@postgres/app' | spinup secrets set --env staging DATABASE_URL
$ spinup secrets get --env staging DATABASE_URL
postgres://app:pw@postgres/app
```

Select the environment with `--env`, and another project with `--dir`. Without `--env`, every command fails and names the flag:

```console
$ spinup secrets init
Error: select secrets environment: --env is missing or invalid
  Try: pass --env <environment>
[exit 1]
```

## How it works

- The encrypted file for an environment is `secrets/<env>.enc.yaml`. It holds one flat map of names to string values.
- `.sops.yaml` lists the recipients. A recipient is the public half of an age key. Each recipient can decrypt the file.
- `init` makes one key for the environment. It lives at `~/.config/spinup/keys/<app>-<env>.agekey` with mode `0600`. The variable `SPINUP_KEYS_DIR` changes the directory.
- A recovery recipient is a second key that you keep outside the repository. It lets you read the secrets if you lose the environment key.
- A CI recipient is a key that only your CI system holds.
- A deploy decrypts the file on your machine and writes the values to the VM. See [deploy](deploy.md).

Secret names use uppercase environment variable syntax, such as `DATABASE_URL`.

## Create the secrets for an environment

First create a recovery key and keep its private half in a safe place:

<!-- doctest: skip -->
```console
$ age-keygen -o /safe/location/recovery.agekey
Public key: age1...
```

Then pass the public key to `init`. Add `--ci-recipient` for a CI key.

<!-- doctest: skip -->
```console
$ spinup secrets init --env staging --recovery-recipient age1... --ci-recipient age1...
```

`init` creates `secrets/staging.enc.yaml` and updates `.sops.yaml`. It keeps your comments.

{: .warning }
`init` needs a recovery recipient. `--no-recovery` skips it and prints a warning.
If you lose every key, nobody can read the secrets again.

Never commit `*.agekey` or `.env`. Add both to `.gitignore`.

## Set, read, and edit secrets

`set` reads the value from standard input and keeps it exactly, including a trailing newline.
`get` writes the exact value to standard output with no extra newline.

```console
$ printf '%s' 'abc123' | spinup secrets set --env staging API_TOKEN
$ spinup secrets get --env staging API_TOKEN
abc123
```

To set a value from a file, redirect the file:

<!-- doctest: skip -->
```console
$ spinup secrets set --env staging TLS_KEY < tls.key
```

`edit` opens the whole file in your sops editor. It needs a terminal.

<!-- doctest: skip -->
```console
$ spinup secrets edit --env staging
```

Without a terminal, `edit` fails and points you to `set`:

```console
$ spinup secrets edit --env staging
Error: edit secrets: a terminal is required
  Try: run in a terminal or use spinup secrets set --env <environment> <NAME>
[exit 1]
```

## See what changed

`diff` compares the secrets with git `HEAD` and prints only names, never values.
The labels are `added`, `removed`, and `changed`, in sorted order.
An environment with no committed file compares against an empty map.
The project must be a git repository.

<!-- doctest: skip -->
```console
$ spinup secrets diff --env staging
added DATABASE_URL
changed API_TOKEN
```

## Push secrets to the VM

A deploy already writes the secrets to `/opt/<app>/secrets/` on the VM.
To change a value without a deploy, run `push`.
It takes the deploy lock, writes one file for each secret with mode `0600`, and removes files for names that no longer exist.
Running containers read the files when they start, so the next deploy or restart picks up the new values.

<!-- doctest: skip -->
```console
$ spinup secrets push --env staging
Pushed 3 secrets to staging: DATABASE_URL, DIND_TOKEN, POSTGRES_PASSWORD
Next: spinup deploy --env staging (running containers read the files at start)
```

On the first connection to a new VM, add `--accept-new-host-key`.
A protected environment asks you to type its name. In CI, set `CI` and pass `--yes`, or pass `--confirm-env <name>`.

<!-- doctest: skip -->
```console
$ spinup secrets push --env staging --accept-new-host-key
```

## Rotate and rekey

Use `rotate` to replace the data key inside the file.
Do it when someone who could read the secrets leaves the team.
Then change the secrets themselves, because the old reader still knows the old values.

Use `rekey` after you add or remove a recipient in `.sops.yaml`.
It re-encrypts the file for the new list.

<!-- doctest: skip -->
```console
$ spinup secrets rotate --env staging
$ spinup secrets rekey --env staging
```

Both commands ask for confirmation in a protected environment. In CI, set `CI` and pass `--yes`, or pass `--confirm-env <name>`.

## Add a teammate

1. Ask the teammate for the public key of their age key (`age1...`).
2. Add it to the matching rule in `.sops.yaml`.
3. Run `spinup secrets rekey --env <name>`.
4. Commit `.sops.yaml` and the encrypted file.

The teammate puts their private key in `~/.config/spinup/keys/<app>-<env>.agekey`.

## Use secrets in the API

The kit package `secrets` reads a secret from a file or from the environment.

```go
password := secrets.Must("POSTGRES_PASSWORD")
token, err := secrets.Load("API_TOKEN")
```

`Load` reads `<NAME>_FILE` first, then `<NAME>`, and strips one trailing newline.
If the file is unreadable, `Load` returns an error even when `<NAME>` has a value.
`Must` panics when `Load` fails.
Neither function logs a value.

## Check for leaks

The full [doctor](doctor.md) run includes the `plaintext-secrets` check.
It fails when git tracks `.env`, a `*.agekey` file, or a file under `secrets/` without sops metadata.
It also fails when `.gitignore` does not exclude `.env`.
The quick doctor run skips this check.

## Reference

| Subcommand | What it does | Example |
| --- | --- | --- |
| `init` | Create the key and the encrypted file. | `spinup secrets init --env staging --no-recovery` |
| `set <NAME>` | Read a value from standard input. | `spinup secrets set --env staging NAME < value.txt` |
| `get <NAME>` | Write one value to standard output. | `spinup secrets get --env staging NAME` |
| `edit` | Edit the file in your editor. | `spinup secrets edit --env staging` |
| `diff` | Show the names that changed since `HEAD`. | `spinup secrets diff --env staging` |
| `push` | Write the secrets to the VM. | `spinup secrets push --env staging` |
| `rotate` | Replace the data key. | `spinup secrets rotate --env staging` |
| `rekey` | Apply the recipients of `.sops.yaml`. | `spinup secrets rekey --env staging` |

| `init` flag | Meaning | Example |
| --- | --- | --- |
| `--recovery-recipient` | The age public key for recovery. | `--recovery-recipient age1...` |
| `--no-recovery` | Accept permanent loss if all keys are lost. | `--no-recovery` |
| `--ci-recipient` | The age public key for CI. | `--ci-recipient age1...` |

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `a target already exists` on `init` | The environment already has a key or a file. Use the existing ones. |
| `sops` cannot decrypt | Your key is not a recipient. Ask a teammate to run `rekey` with your public key. |
| `diff` says the project is not a git repository | Run `git init` and commit once. |

## See also

- [deploy](deploy.md) and [Secrets](../deploy/secrets.md): how secrets reach the VM
- [Telemetry and data handling](../data-handling.md)
- [tools](tools.md): install `sops` and `age`
- Flag reference: [secrets](../site/commands/secrets.md), [init](../site/commands/secrets-init.md), [set](../site/commands/secrets-set.md), [get](../site/commands/secrets-get.md), [edit](../site/commands/secrets-edit.md), [diff](../site/commands/secrets-diff.md), [push](../site/commands/secrets-push.md), [rotate](../site/commands/secrets-rotate.md), [rekey](../site/commands/secrets-rekey.md)
