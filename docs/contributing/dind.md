# Docker-in-Docker deploy suite

The suite proves the promise of milestone M5 on a containerized VM.
`spinup deploy` ships a generated app over SSH with zero downtime, and `spinup rollback` returns to an earlier release.
The test VM is described in ADR 0009 §7.

## The test VM

The VM is the pinned `docker:dind` image with `openssh-server`, `sudo`, `bash`, `curl`, and `python3`.
The files are in `testdata/dind/vm/`.

- `Dockerfile` starts `FROM [[ image "docker-dind" ]]`. The script replaces this marker with the pin from `internal/stackversions/stackversions.yaml`.
- The `deploy` user has UID 1000, passwordless sudo, and membership of the `docker` group.
- SSH permits only key login for `deploy`. Each run makes a new key pair.
- `entrypoint.sh` starts `sshd` and then `dockerd`.
- `cloud-init` is a stub in `/usr/bin`. It prints `status: done` and exits 0.
- `systemctl` is a shim in `/usr/bin`. It turns `reload ssh.service` and `restart ssh.service` into a HUP of `sshd`.

The script builds the image once for each run. The build needs the network for `apk`.
The VM runs privileged. It publishes SSH and HTTP on free ports of `127.0.0.1`.

## What the suite does

The script `testdata/dind/run.sh` does these steps:

1. Build the `spinup` binary and the VM image. Start the VM.
2. Write an `ssh_config` for the host alias `spinup-dind-vm`, and export `SPINUP_SSH_CONFIG`.
3. Generate the API-only app `spinup-dind-app` with `--modules ci,deploy,secrets` and the local kit.
4. Add `[env.staging]` with the VM host, the SSH port, `public_url`, and `transport = "ssh"`.
5. Create the secrets `DATABASE_URL` and `POSTGRES_PASSWORD` with test keys.

Then it tests the commands:

| Step | Command | Check |
| --- | --- | --- |
| 1 | `server bootstrap --plan` and `--print-cloud-init` | Bootstrap connects and refuses the Alpine VM. The cloud-init output holds the key. |
| 2 | `deploy --env staging --yes` | `/healthz` answers through the VM port. `/version` reports the release. |
| 3 | `deploy` of a release whose `/readyz` fails | The deploy exits non-zero. The first release stays live, and only one color runs. |
| 4 | `deploy` of a good release, then `rollback` | `/version` reports the first release again. `releases list` marks it as current. |
| 5 | `deploy` while another process holds the lock | The deploy exits 5 and names the owner. `deploy unlock --force` removes the lock. |
| 6 | `deploy` after `secrets set` of a new name | Each secret file has mode 0600 and the owner `deploy`. The value is not in a release file. |

Step 7 tests `spinup deploy digital-ocean` (ADR 0017). The VM plays the part of a new droplet:

1. The suite builds `testdata/dind/dofake` and starts it. This program serves the DigitalOcean API fake of `internal/cloud/digitalocean/dofake`. It accepts one static token. It also serves `GET /debug/state` with the number of droplets. The `dofake` package does not have this route.
2. The suite adds a `Host 127.0.0.1` block to the `ssh_config`, with the port of the VM and the operator key. The droplet address is `127.0.0.1`, and the new environment has no port.
3. The suite copies `deploy/environments/staging.toml` to `do.toml` and commits it.
4. The suite runs `spinup deploy digital-ocean --env do --public-url http://localhost:<port> --skip-build --tag <release> --no-recovery --yes --ssh-key <key>`. The environment holds `SPINUP_DO_API_BASE` and `SPINUP_DO_OAUTH_BASE` (the fake), `DIGITALOCEAN_ACCESS_TOKEN` (the static token), and `SPINUP_CREDENTIALS_DIR`. No browser sign-in runs.
5. When the fake creates a droplet, it parses the user data and runs `testdata/dind/on-create.sh`. The script installs the injected host key, `/usr/local/sbin/spinup-host-key`, and its sudoers rule on the VM, and makes `sshd` use only the ed25519 key.
6. The suite checks `/version`. It checks that `[env.do]` in `spinup.toml` pins exactly one host key. This key is the key that the VM serves now, and it is not the injected key.
7. A second run must create no droplet.
8. `spinup deploy digital-ocean destroy --env do --i-know --yes` (with `CI=1`) must remove the droplet from the fake.

The step needs `ssh-keyscan` and the `go` toolchain, in addition to the tools of the other steps. The trap stops the fake, because the script keeps its process ID in `pids`.

`testdata/dind/fixtures/broken-readyz.patch` makes the release of step 3.
It adds a readiness check that always fails. `/healthz` still answers, so only the readiness check of the deploy stops the release.

## Run the suite

Docker must run, and the VM must have network access to pull images and build the Postgres image.

```
bash testdata/dind/run.sh
```

`make dind` runs the same script.

| Option | Effect |
| --- | --- |
| `DIND_WEB=1` | Generate a full app (`api` and `web`). The web image build needs the network. |
| `DIND_KEEP=1` | Keep the work directory and its logs. The trap still removes the VM. |
| `GO=<path>` | Use this `go` binary. |

The script also needs `git`, `ssh`, `ssh-keygen`, `ssh-keyscan`, `sops`, `age-keygen`, `curl`, and `python3`.
Run `spinup tools install` to get `sops` and `age`.

## Limits

- `server bootstrap` supports only Debian and Ubuntu. The VM is Alpine, so the script makes `/opt/<app>` and its directories itself. It does what bootstrap does for these directories.
- The script compiles the API on the host and copies it into the distroless base of `deploy/Dockerfile.api`. The kit replace is a local path outside the Docker build context, so `spinup build` cannot build the image. Each deploy uses `--skip-build --tag <release-id>`.
- spinup has no `secrets push` command. The `upload` step of `deploy` pushes the secrets, so step 6 checks the files after a deploy.

## Clean up

Every container, volume, and image of a run has a name that starts with `spinup-dind-`.
A trap removes them when the script stops, also after a failure.
The script never touches other containers.

If a run fails, read the logs in the work directory that the script prints.
`deploy.state` in that directory is the deploy journal of the VM.

## CI

`.github/workflows/dind.yml` runs the suite on each push to `main` and each night.
It runs the API-only app and the full app as two jobs.
