---
title: Secrets
parent: Deploy
nav_order: 4
---

# Secrets

Secrets are the passwords and keys of your app, such as `DATABASE_URL`.
spinup keeps them encrypted in git, one file for each environment, so you can review changes and still keep the values private.

<!-- doctest: skip -->
```console
$ age-keygen -o /safe/place/recovery.agekey # store this file on another machine or in a password manager
$ spinup secrets init --env staging --recovery-recipient age1...
$ printf '%s' 'postgres://app:pw@postgres:5432/app?sslmode=disable' | spinup secrets set --env staging DATABASE_URL
```

This needs `sops` and `age-keygen` on your machine. Run `spinup tools install` if you miss them.

## How it works

- The file `secrets/<env>.enc.yaml` holds the secrets of one environment. It is encrypted with [sops](https://github.com/getsops/sops) and [age](https://github.com/FiloSottile/age). You commit it.
- `.sops.yaml` lists who can decrypt each file. These people or machines are the recipients.
- The private key of an environment stays on your machine at `~/.config/spinup/keys/<app>-<env>.agekey`. Never commit it.
- A recovery key is a second recipient that you keep outside the repository. It saves you if you lose your own key.
- On a deploy, spinup decrypts the file on your machine and uploads one file for each secret to `/opt/<app>/secrets/` on the server. Containers read them from `/run/secrets`.

{: .warning }
If you lose every recipient key, nobody can decrypt the file. Keep the recovery key in a safe place.

A secret name uses uppercase letters, digits, and underscores, such as `DATABASE_URL`.
The decrypted file is one flat map of names to strings.

## Create the secrets of an environment

Make a recovery key once, and keep the private key away from the repository.
Then run `init`:

<!-- doctest: skip -->
```console
$ age-keygen -o /safe/place/recovery.agekey
Public key: age1...
$ spinup secrets init --env staging --recovery-recipient age1...
```

`init` creates the environment key and `secrets/staging.enc.yaml`.
It adds a rule to `.sops.yaml` and keeps the comments.
Without a recovery recipient, `init` fails.
To accept the risk, pass `--no-recovery`. spinup prints a warning.

Without an environment, the command tells you what to pass:

```console
$ spinup secrets init
Error: select secrets environment: --env is missing or invalid
  Try: pass --env <environment>
[exit 1]
```

A deploy needs `DATABASE_URL` and `POSTGRES_PASSWORD`, and `GRAFANA_ADMIN_PASSWORD` when monitoring is on.
`spinup deploy digital-ocean` creates them for you.

## Add or change a secret

`set` reads the value from standard input, so it never appears in your shell history or process list:

<!-- doctest: skip -->
```console
$ printf '%s' 'S3cret!' | spinup secrets set --env staging POSTGRES_PASSWORD
```

`set` keeps the input exactly, including a trailing newline.
Use `printf '%s'` to avoid an extra newline.

To edit all the values in your editor, run `edit`. It needs a terminal:

<!-- doctest: skip -->
```console
$ spinup secrets edit --env staging
```

Without a terminal, `edit` fails and names `set` as the fix:

```console
$ spinup secrets edit --env staging
Error: edit secrets: a terminal is required
  Try: run in a terminal or use spinup secrets set --env <environment> <NAME>
[exit 1]
```

## Read and compare

`get` writes one value to standard output with no extra newline:

<!-- doctest: skip -->
```console
$ spinup secrets get --env staging DATABASE_URL
postgres://app:pw@postgres:5432/app?sslmode=disable
```

`diff` compares the decrypted names with git `HEAD`. It prints names only, never values:

<!-- doctest: skip -->
```console
$ spinup secrets diff --env staging
added DATABASE_URL
```

The labels are `added`, `removed`, and `changed`, in sorted order.
An environment with no committed file compares against an empty map.

## Send secrets to the server

Every `spinup deploy` uploads the current secrets.
To change a value without a deploy, run `push`:

<!-- doctest: skip -->
```console
$ spinup secrets push --env staging
Pushed 3 secrets to staging: DATABASE_URL, DIND_TOKEN, POSTGRES_PASSWORD
Next: spinup deploy --env staging (running containers read the files at start)
```

`push` takes the deploy lock, writes each file with mode 0600, and removes files for names that no longer exist.
Running containers read the files when they start.
Run `spinup deploy` or restart the containers to use a new value.

## Rotate keys

`rotate` replaces the data key that encrypts the file.
Run it on a schedule, or after someone leaves the team.

<!-- doctest: skip -->
```console
$ spinup secrets rotate --env staging
```

Rotation does not change the secret values.
If a person left, also change the values that they could read.

## Add a teammate

1. Ask the teammate for their age public key (`age-keygen` prints it).
2. Add the key to the `age` list of the rule in `.sops.yaml`.
3. Run `rekey` so the file is encrypted for the new list.
4. Commit `.sops.yaml` and the file.
5. The teammate saves their private key at `~/.config/spinup/keys/<app>-<env>.agekey`. spinup always reads the key from this path.

<!-- doctest: skip -->
```console
$ spinup secrets rekey --env staging
```

To remove a person, delete their key from `.sops.yaml`, run `rekey`, and then `rotate`.
Both commands ask for confirmation in a protected environment. In CI, pass `--yes`.

## Use secrets in CI

CI needs its own key, so you never share a personal key.

1. Make a key for CI: `age-keygen -o ci.agekey`.
2. Pass its public key at `init` with `--ci-recipient age1...`. For an existing file, add the key to `.sops.yaml` and run `rekey`.
3. Store the private key as the GitHub secret `SPINUP_AGE_KEY`.

The [generated CI](../ci.md) page has the full setup.

## Read secrets in your Go code

The kit reads a secret from the file that `NAME_FILE` names, or else from the variable `NAME`:

```go
import "github.com/stopyellingatme/spinup/kit/secrets"

url, err := secrets.Load("DATABASE_URL") // error if the file cannot be read
key := secrets.Must("SESSION_KEY")       // panics on error
```

Both functions remove one trailing newline and never log a value.

## Reference

| Command | What it does |
| --- | --- |
| `secrets init` | Create the environment key and the encrypted file. |
| `secrets set NAME` | Set one secret from standard input. |
| `secrets get NAME` | Print one secret. |
| `secrets edit` | Edit all secrets in your editor. Needs a terminal. |
| `secrets diff` | Show the names that changed since git `HEAD`. |
| `secrets push` | Write the secrets to the server without a deploy. |
| `secrets rotate` | Replace the data key. |
| `secrets rekey` | Encrypt again for the recipients in `.sops.yaml`. |

`SPINUP_KEYS_DIR` changes the folder of the keys (mode 0700, key files 0600).

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `--env is missing or invalid` | No environment was given. | Pass `--env <name>`. |
| `a terminal is required` | `edit` runs without a terminal. | Use `secrets set`. |
| `sops` cannot decrypt | Your key is not a recipient. | Ask a teammate to run `rekey` with your public key. |
| `spinup doctor` reports `plaintext-secrets` | A `.env`, `*.agekey`, or an unencrypted `secrets/*.yaml` is tracked, or `.gitignore` misses `.env`. | Untrack the file and add it to `.gitignore`. |

## See also

- [Environments](environments.md)
- [Deploy to your own server](../deploy.md)
- [The `secrets` command guide](../commands/secrets.md)
- [Flag reference of `spinup secrets`](../site/commands/secrets.md)
