# ADR 0017: `spinup deploy digital-ocean`

Status: accepted (2026-10-22)

Before this ADR, a first deploy needed manual work. The user created a VM, ran `spinup server bootstrap` or pasted the cloud-init file, added `[env.<name>]` to `spinup.toml`, set `DATABASE_URL` and `POSTGRES_PASSWORD`, and pointed DNS at the VM. `spinup infra` supports only Hetzner, through OpenTofu.

This ADR adds one command that takes an app from no server to a live deploy on DigitalOcean, with a browser sign-in. ADRs 0001 to 0016 still apply. `spinup infra` does not change.

## 1. Commands

```
spinup deploy digital-ocean [flags]         # aliases: digitalocean, do
spinup deploy digital-ocean login [--no-browser]
spinup deploy digital-ocean logout
spinup deploy digital-ocean status [--json]
spinup deploy digital-ocean destroy --i-know
```

- The main command creates the resources that are missing, writes the environment, makes the missing secrets, commits, and then runs the normal deploy (ADR 0009).
- After the first run, plain `spinup deploy` works for the environment. It needs no DigitalOcean token, so CI does not change.
- Flags of the main command: `--region`, `--size`, `--image`, `--domain`, `--public-url`, `--ssh-key`, `--ssh-from`, `--no-browser`, `--recovery-recipient`, `--no-recovery`, `--plan`, `--skip-deploy`, `--no-commit`, `--recreate`, `--i-know`, `--dns-timeout`.
- The command also accepts the deploy flags `--allow-dirty`, `--allow-destructive`, `--skip-build`, `--tag`, `--platform`, `--health-timeout`, `--no-auto-rollback`. One helper, `addDeployFlags`, defines them for both commands.
- Before it creates a droplet, the command asks for the price once ("Create droplet s-2vcpu-4gb in nyc3 for $24.00/month?"). `--yes` answers it. With no TTY and no `--yes`, the command fails and names `--yes`.
- The command refuses `--offline`.
- `destroy` requires `--i-know`, and the user types the environment name. It deletes only the resources that have the environment tag and the IDs in `spinup.toml`. It keeps the account SSH key, the local secrets, and the age key. It deletes the A record only when the record points at the droplet. It says that the database is lost and names `spinup backup`.

## 2. Sign-in

- OAuth 2.1 authorization code with PKCE (S256), as a public client. The client has no secret.
- Endpoints: `https://cloud.digitalocean.com/v1/oauth/{authorize,token,revoke}`. `SPINUP_DO_OAUTH_BASE` overrides the base for tests.
- The CLI listens on `127.0.0.1:0` and uses `http://127.0.0.1:<port>/callback`. DigitalOcean matches loopback redirect URIs on any port.
- The CLI checks `state` in constant time and handles `error=`. The login stops after 5 minutes.
- `--no-browser` prints the URL only. `SPINUP_BROWSER` is a command that opens the URL (tests use `curl -fsSL`).
- The client ID is the constant `digitalocean.DefaultClientID`. `SPINUP_DO_CLIENT_ID` overrides it. A public client ID is not a secret. spinup never uses the doctl client ID.
- The maintainers register the client once with `scripts/do-register-client.sh` (`POST /v1/oauth/register`, `token_endpoint_auth_method: none`). The registration access token is never committed.
- Access tokens last 1 hour. Refresh tokens are single-use and rotate. A refresh sends `client_id` and no secret.

Token order:
1. `DIGITALOCEAN_ACCESS_TOKEN`, then `DIGITALOCEAN_TOKEN` (a personal access token). spinup never stores these.
2. The stored token. spinup refreshes it when it expires in less than 5 minutes.
3. A browser login, in a TTY only.
4. Otherwise an error: `Try: export DIGITALOCEAN_ACCESS_TOKEN, or run spinup deploy digital-ocean login in a terminal`.

Token store:
- `~/.config/spinup/credentials/digitalocean.json` (file 0600, directory 0700), or the same name in `SPINUP_CREDENTIALS_DIR`.
- spinup writes the file with a temporary file and a rename.
- A `flock` on `digitalocean.json.lock` covers each refresh. The process reads the file again after it gets the lock. Two processes never use the same refresh token.
- `invalid_grant` deletes the file and asks for a new login.
- `logout` revokes the refresh token and the access token, then deletes the file.
- spinup does not use the macOS `security` command, because it puts the secret in argv. An OS keychain is later work.

## 3. API client

- `internal/cloud/digitalocean` uses `net/http` and the DigitalOcean API v2 (`SPINUP_DO_API_BASE` overrides the base for tests). A base that is not https must be a loopback address.
- The client follows pagination links. It retries a 429 after `Retry-After` (or `RateLimit-Reset`). It retries a 5xx or a network error with backoff, for idempotent calls only. It never retries a POST. The next run looks the resource up again.
- It looks up an SSH key by fingerprint, never by a list. The list endpoint allows 10 requests a minute.
- API errors carry the status, the error ID, the message, and the request ID. 401 says to sign in again. 403 names a missing scope. 422 on a droplet create names the droplet limit.

## 4. Resources

- Tags: `spinup` and `spinup:<app>:<env>`. App names and environment names cannot contain a colon, so the tag is not ambiguous.
- The tag finds the droplet. `droplet_id` in `spinup.toml` checks it. More than one tagged droplet is an error.
- Droplet: name `<app>-<env>`, the image, region, and size, the SSH key, `ipv6: true`, no DigitalOcean backups or monitoring, the default VPC, and the cloud-init user data.
- Firewall `spinup-<app>-<env>`, applied by tag: 22/tcp from `--ssh-from` (default all), 80/tcp, 443/tcp, and 443/udp from all, all outbound. Docker-published ports bypass ufw, so the Cloud Firewall is the real filter.
- Defaults are in `stackversions.yaml` (`digitalocean:`). A production environment uses `size_production` (`s-2vcpu-4gb`), because `production.toml` gives postgres `cpus = "2.0"`. Other environments use `size_default` (`s-1vcpu-2gb`).
- Size check: vCPUs ≥ the largest `cpus` value in `deploy/environments/<env>.toml`, and memory ≥ 2 GiB.
- Only Ubuntu images are accepted. The cloud-init template is Ubuntu only.
- Scopes: `account:read regions:read sizes:read image:read actions:read droplet:create droplet:read droplet:delete ssh_key:create ssh_key:read firewall:create firewall:read firewall:update firewall:delete tag:create tag:read domain:read domain:create domain:update domain:delete`. The live test confirms the exact strings.

## 5. spinup.toml

```toml
[env.production]
host = "203.0.113.7"
user = "deploy"
public_url = "https://203-0-113-7.sslip.io"
host_keys = ["ssh-ed25519 AAAA..."]

[env.production.digitalocean]
droplet_id = 512345678
region = "nyc3"
size = "s-2vcpu-4gb"
image = "ubuntu-24-04-x64"
tag = "spinup:app:production"
firewall_id = "..."
team = "<team uuid>"
domain = "app.example.com"
```

- `project.Env.HostKeys` (`host_keys`) is a generic field. Only `internal/provision` reads the `digitalocean` table.
- Writes use `codemod.SetTOML` and an atomic replace, so comments stay.
- `default_env` is written only when it is not set.
- The stored `team` must equal the team of the token. A token of a different team fails. It cannot create a second droplet in another team.

## 6. Host keys

- Before the create POST, spinup generates an ed25519 host key with `ssh-keygen` and writes it to `host_keys`. The user data gives it to cloud-init in `ssh_keys`. The first connection is pinned. There is no trust on first use.
- When `remote.Host.HostKeys` is set, every remote command uses its own known_hosts file (`<cache>/spinup/ssh/known_hosts-<hash>`, lines `* <key>`) with `StrictHostKeyChecking=yes`, `GlobalKnownHostsFile=/dev/null`, `UpdateHostKeys=no`, `CheckHostIP=no`, and `HostKeyAlgorithms=ssh-ed25519`. The pin wins over `--accept-new-host-key`.
- The injected private key stays readable in the droplet metadata. So spinup replaces it at once, in two phases. cloud-init installs `/usr/local/sbin/spinup-host-key` (`next`, `activate`) and a sudoers line that `visudo -cf` checks:
  1. `next` makes the next key. spinup pins the old key and the next key.
  2. `activate` moves the next key into place and reloads sshd. spinup connects again.
  3. spinup pins only the new key.
  A crash at any point is safe, because both keys are valid during the change.
- A host key mismatch is fatal and is never retried.

## 7. Steps of a run

Each step reads the current state and makes it correct. A second run continues after a crash. The run keeps no local journal.

1. Local preflight, before any cost: the project, the deploy pack, `deploy/cloud-init/user-data.yaml`, the tools (`ssh`, `ssh-keygen`, `sops`, `age-keygen`, `git`, `docker info`), a clean tree, and the size.
2. The token, then `GET /v2/account`, and the team check.
3. The SSH key.
4. The firewall.
5. The host key pin, then the droplet (find by tag, or create).
6. Wait for `active`. Write `host`, `user = "deploy"`, `public_url`, and the `digitalocean` table.
7. DNS (section 8).
8. Wait for SSH with backoff, up to 5 minutes.
9. `cloud-init status --wait --long`. Exit 0 continues. Exit 2 warns. Exit 1 fails, prints the errors, and says `Try: spinup deploy digital-ocean --recreate`.
10. `ssh -O exit`, then a new connection. The deploy user joins the docker group during cloud-init.
11. The host key change (section 6).
12. Secrets (section 9).
13. A commit of `spinup.toml`, `.sops.yaml`, and `secrets/<env>.enc.yaml`: "spinup: provision <env> on DigitalOcean". `--no-commit` stops before the deploy, unless `--allow-dirty` is set.
14. The deploy (ADR 0009). After a failure, `spinup deploy --resume` continues.

Any failure after step 5 prints the droplet, its price for each hour, and the destroy command.

`--recreate` deletes the droplet and creates a new one with new host keys. When a release exists, it needs `--i-know`.

## 8. Domain and TLS

- Without `--domain`, `public_url` is `https://<a-b-c-d>.sslip.io` (the `ip_dns` pin). Caddy gets a Let's Encrypt certificate at once.
- With `--domain`, spinup finds the zone in DigitalOcean DNS by the longest suffix. It creates or updates the A record (TTL 300). When DigitalOcean does not hold the zone, spinup prints the record and waits for it to resolve, up to `--dns-timeout` (10 minutes). spinup warns about an AAAA record that points to a different host.
- A change of the domain needs a new web image, because the image holds `PUBLIC_ORIGIN`. When the app has a web image, spinup refuses `--skip-build` if the public URL changes.
- sslip.io is probably not on the Public Suffix List. All its users share one Let's Encrypt limit. `nip.io` is the fallback.

## 9. Secrets

- When `secrets/<env>.enc.yaml` does not exist, spinup runs `secrets.Init` with `--recovery-recipient` or `--no-recovery`. With neither flag, spinup fails in the local preflight, before any cost, with a `Try:` line. It does not ask.
- `deploy.RequiredSecrets(root, config)` gives the names. spinup makes only the missing values, from 32 random bytes (base64url): `POSTGRES_PASSWORD`, `DATABASE_URL` (`postgres://app:<pw>@postgres:5432/app?sslmode=disable`), and `GRAFANA_ADMIN_PASSWORD` when monitoring is on.
- spinup never replaces a value. When only `DATABASE_URL` exists, spinup takes the password from it.
- spinup prints the names only, never the values. It does not make backup secrets.

## 10. Tests

- `dofake` is an httptest fake of the API and the OAuth endpoints. It checks PKCE and rotates refresh tokens. It can inject failures and rate limits.
- testscript `deploy_digitalocean` runs the fake in the harness and signs in with `SPINUP_BROWSER=curl -fsSL`.
- dind: a `dofake` binary gives the dind VM as the droplet.
- A live test on a real account runs after the merge. It needs approval for each paid step.
