# ADR 0009: M5 contracts (deploy, server, and secrets)

Status: accepted (2026-10-15)

The M5 units run in parallel. These decisions fix the contracts between them. ADR 0003 (from the M5.1 spike) fixes the switch strategy: blue/green Compose projects, or a rolling fallback.

## 1. Remote access (`internal/remote`, unit M5.2)

- spinup drives the system `ssh` binary through `internal/exec` with the user's SSH config, never a Go SSH library. Connection sharing: `-o ControlMaster=auto -o ControlPath=<user cache dir>/spinup/ssh/%C -o ControlPersist=60s`. Host key checking stays on (`StrictHostKeyChecking=accept-new` only with `--accept-new-host-key`). `SPINUP_SSH_CONFIG=<file>` adds `ssh -F <file>` (test VMs and CI).
- `remote.Host` comes from an environment in `spinup.toml` (`[env.<name>]`: `host`, `user`, `port`, `public_url`, `protected`).
- `remote.Runner` runs one remote command (argv quoted with POSIX shell rules), streams stdout and stderr, and uploads bytes to a path with a mode (`ssh ... 'umask 077 && cat > <tmp> && chmod <mode> <tmp> && mv <tmp> <path>'`). A fake implements it for tests.

## 2. Layout on the VM

```
/opt/<app>/
  releases/<release-id>/   compose.yml, Caddyfile, Caddyfile.d/, release.json (no secrets)
  current -> releases/<release-id>
  secrets/<NAME>           one file per secret, mode 0600, owned by the deploy user
  data/                    named volumes live in Docker; this holds bind data (Caddy certs)
  backups/                 pgBackRest spool and local copies
  deploy.lock/             the deploy lock (a directory, created atomically with mkdir)
  deploy.state             the journal of the current deploy (JSON lines)
  releases.json            the release history (newest first, keep 5)
```

- A release ID is `<UTC time YYYYMMDDTHHMMSSZ>-<12-char git SHA>`.
- The lock: `mkdir /opt/<app>/deploy.lock` then write `owner.json` (`{"owner","pid","host","acquired","ttl_seconds","release"}`). A held, unexpired lock makes `deploy` exit 5 (`ui.ExitLocked`) and print the owner. An expired lock may be taken over with a warning. `deploy unlock --force` removes it.
- The journal: one JSON object per line, `{"release","step","status":"started|done|failed","at","detail"}`. `deploy --resume` continues from the first step that is not done. `deploy status` prints the current release, the lock, and the last journal.

## 3. Hooks (`internal/deploy`, unit M5.2)

`deploy.RegisterPreDeploy(Hook)` and `deploy.RegisterPostRelease(Hook)`, with `type Hook struct { Name string; Run func(ctx context.Context, c *HookContext) error }` and `HookContext{Env project.Env; App string; Release string; Remote remote.Runner; Out io.Writer}`. Hooks run in name order. Unit M5.7 registers the pre-deploy backup; unit M6.3 registers the Grafana annotation.

## 4. Images

- `spinup build --image` (M1.4) builds local images. Unit M5.9 adds `--push` (buildx, GHCR, provenance and SBOM) and `--platform` (default from the environment, `linux/amd64`).
- Image transport per environment (`[env.<name>] transport = "ssh" | "registry"`, default `ssh`): `ssh` streams `docker save` into `docker load` on the VM (no registry needed; the DinD suite uses it); `registry` pushes and the VM pulls by digest. The deployer never uses `latest`. Images are tagged `<app>-api:<release-id>` and `<app>-web:<release-id>`.

## 5. Secrets (`internal/secrets`, kit `secrets`, unit M5.3)

- `.sops.yaml` (scaffold) holds one creation rule per environment: `secrets/<env>.enc.yaml` encrypted to the environment key, a recovery recipient, and a CI recipient (critic resolution 12).
- `secrets/<env>.enc.yaml` (seed) is a flat map `NAME: value`. Private keys live in `~/.config/spinup/keys/<app>-<env>.agekey` (0600). spinup runs the `sops` and `age-keygen` binaries (`spinup tools install` provides checksum-verified copies, unit M5.10).
- On deploy and `secrets push`, spinup decrypts locally and writes each value to `/opt/<app>/secrets/<NAME>` (0600). Compose mounts that directory read-only at `/run/secrets`, and the app reads `<NAME>_FILE=/run/secrets/<NAME>` (kit `config` already supports `_FILE`). Secret values never enter an image, a Compose file, a release snapshot (only their SHA-256), or a log.

## 6. Production Compose (pack `deploy`, unit M5.4)

- `deploy/compose.yml` (managed) includes `compose.override.yml` (seed). Services: `caddy` (the only published ports, 80 and 443, label `spinup.public: "true"`), `api`, `web`, `postgres` (built from `deploy/Dockerfile.postgres` with pgBackRest, M5.7), and a one-shot `migrate` (profile `migrate`, runs the API binary with `migrate up`).
- The API gets `SPINUP_ENV=production`, `PUBLIC_ORIGIN=<public_url>`, `DATABASE_URL_FILE=/run/secrets/DATABASE_URL`. The web gets `API_URL=http://api:8080` and is built with `PUBLIC_ORIGIN` (ADR 0002 rule 2).
- Every container: `read_only: true` where the image allows it, `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`, a non-root user, a health check, and log rotation (`max-size: 10m`, `max-file: "3"`). The file must pass the Compose security lint.
- `deploy/Caddyfile` (managed) imports `Caddyfile.d/*.caddy` (seed directory). Caddy routes `/api/*` to `api:8080` and everything else to `web:3000`.

## 7. Test VM (unit M5.8)

The DinD suite runs a privileged `docker:dind` container with `openssh-server` and a `deploy` user as the "VM", reachable on a local SSH port. It tests bootstrap, deploy, a failed health check with automatic rollback, `rollback`, lock contention, and `secrets push`.
