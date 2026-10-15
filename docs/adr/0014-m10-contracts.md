# ADR 0014: M10 contracts (deferred full-depth items)

Status: accepted (2026-10-15)

M10 is opt-in work that the core does not depend on. The critique wanted these items out of v1. The plan keeps them, so each one ships behind an explicit flag or module, with the limits below written in its docs. These decisions fix the contracts between the parallel units.

## 1. Tier-2 plugins (units M10.1 and M10.2)

- A tier-2 plugin is a plugin (ADR 0013 §4) whose `plugin.toml` has `kind = "exec"` and `[exec] binaries = { "linux-amd64" = "bin/<name>_linux_amd64", ... }`. The binaries are inside the plugin tree, so the tree hash covers them. `kind = "pack"` (the default) keeps tier-1 behavior.
- Trust: tier-2 plugins run with the full privileges of the user (critic resolution 18). `plugin add` of an exec plugin needs a typed confirmation (type the plugin name) on a TTY, or `--trust-exec <name>` without one. The allowlist line ends with ` exec`. This amends ADR 0013 §4: spinup runs plugin code only for `kind = "exec"` plugins, and says so in the docs.
- Protocol: JSON-RPC 2.0 over the plugin's stdin and stdout, one JSON object per line, implemented with `encoding/json` (no library). Methods: `handshake` (`{name, version, api, capabilities}`; the host supports API 1), `describe` (generators and checks), `plan` (a generator call: arguments in, `{files: [{path, data_base64, class, mode}], next: [...]}` out), `check` (`{findings: [...]}`). Capabilities in API 1: `generator` and `check`.
- The host never lets a plugin write: it validates every returned path with `pack.CheckOutputPath` and applies the plan through the generator runner. It starts the plugin with a scrubbed environment (`PATH`, `HOME`, `LANG`, `TMPDIR`, `SPINUP_PLUGIN_API=1`), the project root as working directory, no secrets, a 60-second timeout per call, and a 16 MiB response cap.
- The SDK is the module `github.com/stopyellingatme/spinup/plugin-sdk` in `plugin-sdk/` (its own `go.mod`, joined by `go.work`): package `sdk` (protocol types and `Serve`) and `sdk/conformance` (a test kit that runs a plugin binary through every method, including malicious cases). It has no dependencies.
- `internal/cli/plugin.go` gains `registerPlugin(build)` (pre-seed), so `plugin new` lives in its own file.

## 2. Embed mode (unit M10.3)

- The web pack question `frontend_mode` (`node` default, `embed`) selects it. In embed mode the web app builds with `@sveltejs/adapter-static` as a single-page app (`fallback: 'index.html'`, `ssr = false` in the root layout), and the API binary embeds the build with `//go:embed` behind the build tag `embedweb`. kit `app` gains `WithSPA(fs.FS)`: it serves the assets with long cache headers for hashed files and `index.html` for every other non-`/api` path.
- One binary serves both on one origin, so the session cookie works without changes. Deploy: no `web` service, and Caddy sends every path to the API.
- Limits (documented, enforced with a `Try:` line): embed mode refuses the `auth` module (its pages use SvelteKit form actions) and the page generators that write `+page.server.ts`.
- The web `package.json` always lists `@sveltejs/adapter-static` (pre-seed), so the lockfile stays a superset.

## 3. Package manager and API client variants (unit M10.4)

- Web pack questions `package_manager` (`pnpm` default, `bun`) and `api_client` (`openapi-fetch` default, `heyapi`). bun is pinned at 1.4.2. The pre-seed makes the web `package.json` template follow `package_manager` (`packageManager` field and scripts) and ships both `pnpm-lock.yaml` and `bun.lock`, each rendered only for its manager. `@hey-api/openapi-ts` is always a dev dependency.
- `internal/webtool` (unit M10.4) is the one place that knows the commands: install (frozen and offline variants), run, exec, and the client generator. Every caller that runs `pnpm` today uses it.

## 4. OpenTofu for Hetzner Cloud (unit M10.5)

- A module pack `tofu` renders `deploy/infra/tofu/` (`versions.tf`, `main.tf`, `variables.tf`, `outputs.tf` managed; `terraform.tfvars.example` seed; a `.gitignore` for state). It creates a server, a firewall (22, 80, 443), a volume, and an SSH key. DNS is out of scope: the docs say to point a DNS record at the `ipv4` output.
- OpenTofu 1.13.1 (checksums in `stackversions.yaml`; `spinup tools install` installs it) and the provider `hetznercloud/hcloud` 1.70.0.
- `spinup infra plan|apply|output|destroy --env <name>` runs `tofu -chdir=deploy/infra/tofu` through `internal/exec` with `HCLOUD_TOKEN` from the environment. `apply` writes the `ipv4` output into `[env.<name>] host` (comment-preserving). `destroy` calls `project.ConfirmProtected`.

## 5. Self-update (unit M10.6)

- `spinup self-update [--check] [--channel stable|next] [--version X]` downloads the release archive and `checksums.txt` (ADR 0012 §1), verifies the SHA-256, verifies `checksums.txt.sigstore.json` with the `cosign` binary when it is on `PATH` (identity `https://github.com/stopyellingatme/spinup/.github/workflows/release.yml@refs/tags/v<version>`, issuer `https://token.actions.githubusercontent.com`; `--require-signature` makes a missing cosign an error), and replaces the binary by an atomic rename.
- A binary inside a Homebrew prefix or Caskroom is never replaced: the command prints `brew upgrade --cask spinup`. Goreleaser ships a cask (this corrects ADR 0012 §3, which says formula).
- `spinup version --check` and a daily notice on stderr, off in CI, without a TTY, with `--json`, and with `SPINUP_NO_UPDATE_CHECK=1`. The CLI sends no telemetry: the check is one anonymous GET of the GitHub releases API.

## 6. WAL-G backend (unit M10.7)

- `[backups] backend = "walg"` registers through `backup.RegisterBackend`. WAL-G 3.0.9 is installed into the postgres image from the pinned release asset (SHA-256 checked in `Dockerfile.postgres`).
- Secrets: `WALG_S3_PREFIX`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_ENDPOINT`, `AWS_REGION`, `AWS_S3_FORCE_PATH_STYLE`, and `WALG_LIBSODIUM_KEY` (client-side encryption, required).
- The backups pack adds `deploy/compose.backups-walg.yml` (`archive_command = 'wal-g wal-push %p'`). The deployer loads exactly one backup override: `compose.backups.yml` for pgbackrest, `compose.backups-walg.yml` for walg, none for pgdump. `run.sh` and `project.Config.Validate` accept `walg`.

## 7. In-place pg_upgrade (unit M10.8)

- `spinup maintenance pg-major-upgrade --to <major> --in-place` uses `pg_upgrade --link` instead of dump and restore. It builds a helper image on the VM from the target major's pinned image plus the old major's binaries (`COPY --from=<old image> /usr/lib/postgresql/<old> /usr/lib/postgresql/<old>`), takes a full backup with the configured backend first, runs `pg_upgrade --check` and stops on any problem, then upgrades into a new data directory on the same volume and keeps the old one until `--cleanup`.
- The same window, lock, confirmation, and repository steps as the dump and restore path (ADR 0011 §6). With pgBackRest it runs `stanza-upgrade` and then a full backup.

## 8. Docs site (unit M10.9)

- `spinup docs compat [--json|--markdown]` prints the compatibility matrix from `internal/compat` and `stackversions.yaml`. `spinup docs reference --out <dir>` writes one Markdown page per command from the cobra tree (no new dependency).
- `docs/site/` holds generated pages (`compat.md`, `stack.md`, `commands/*.md`) and an index. A test fails when they are stale (update with `SPINUP_GOLDEN_UPDATE=1`). The workflow `.github/workflows/docs.yml` publishes `docs/` with Jekyll through the pinned Pages actions.
