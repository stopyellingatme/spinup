# ADR 0013: M9 contracts (modules, mail, OIDC, and pack plugins)

Status: accepted (2026-10-15)

The M9 units run in parallel. These decisions fix the contracts between them. Architecture §18 and critic resolution 18 still apply.

## 1. Modules (`internal/modules`, unit M9.1)

- A module is a built-in pack that is not `base`, `api`, or `web`. Pack paths stay in `internal/templates/packs/`.
- `modules.All()` returns the module metadata from the embedded packs: name, description, `requires`, and whether it needs the web pack. `modules.Enabled(cfg *project.Config, name string) bool` is the one test for "is this module on". The `web` pack is not a module: `modules.WebEnabled(lock *manifest.Lock) bool` reads `lock.Packs`. Unit M9.1 replaces the hand-written `slices.Contains(cfg.Modules, ...)` checks with these functions.
- `spinup module list` prints the modules, which are on, and what each requires. `spinup module add <name>...` turns modules on in an existing app: it re-renders at the locked template version with the new `modules` answer (and the new packs' question answers, asked or from flags) through the upgrade engine's worktree, plan, and apply, so the user reviews a branch `spinup/module-<name>` exactly as with `spinup upgrade`. `module remove` is out of scope (files may hold user data).

## 2. Mail (kit `mail`, pack `mail`, unit M9.2)

- kit `mail` wraps `github.com/wneessen/go-mail`: `type Mailer interface { Send(ctx context.Context, m Message) error }`, an SMTP mailer configured from `SMTP_URL` (`smtp://user@host:port`, password from `SMTP_PASSWORD` or `SMTP_PASSWORD_FILE`), a log mailer (development without SMTP), and a capture mailer for tests. In development, `spinup dev` already runs Mailpit (SMTP on 1025, UI on 8025).
- Templates are `api/internal/mail/templates/<name>.{subject,txt,html}.tmpl` (Go `text/template` and `html/template`), embedded in the binary. `g mailer <name> [field...]` adds the three templates and a typed method `func (m *Mailers) Send<Name>(ctx, to string, data <Name>Data) error` in `api/internal/mail/<name>_gen.go`.
- When the `jobs` module is on, sends go through a River job (`mail.send`) with retries. Otherwise they are synchronous.
- Password reset (an auth pack fragment, `when = "modules contains 'mail'"`): a migration adds `password_resets(token_hash, user_id, expires_at, used_at)`. `POST /api/auth/reset/request` always answers 202 (no account enumeration) and mails a link valid for one hour. `POST /api/auth/reset/confirm` sets the password once, ends every session of the user, and clears the lockout. Web pages `/reset` and `/reset/[token]`, and a "Forgot password?" link on the login page.

## 3. OIDC (kit `auth`, unit M9.3)

- Generic OpenID Connect with `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2`: authorization code flow with PKCE, `state` and `nonce` in a short-lived, HTTP-only, SameSite=Lax cookie, ID token verification against the issuer.
- Providers come from the environment: `AUTH_OIDC_PROVIDERS=google,acme`, and per provider `AUTH_OIDC_<NAME>_ISSUER`, `_CLIENT_ID`, `_CLIENT_SECRET` (or `_CLIENT_SECRET_FILE`), and optional `_SCOPES` and `_LABEL`.
- Routes: `GET /api/auth/oidc/{provider}/start` and `GET /api/auth/oidc/{provider}/callback`. A migration adds `user_identities(provider, subject, user_id, email)` with a unique `(provider, subject)`, and makes `users.password_hash` nullable. A login links to an existing user only when the provider says the email is verified. The session starts with the existing `startSession` (rotation included).
- The auth pack fragment `pack.d/oidc.toml` adds the question `auth_oidc` (bool, default false), the migration, and the "Sign in with ..." buttons on the login page.

## 4. Pack plugins (`internal/plugin`, units M9.4 and M9.5)

- A plugin is a pack directory (`pack.toml`, `pack.d/`, `files/`) plus `plugin.toml`: `name`, `version`, `api = 1`, `spinup` (a version range of the CLI), `description`, and optional `[[generators]]` (declarative generators: `name`, `short`, `usage`, `args` names, and `[[generators.files]]` with `path`, `source`, `class`; paths and sources are templates over the arguments).
- `spinup.toml` declares `[[plugins]] name, source, version`. Sources: a local directory (`path:./plugins/foo`) or a git repository and tag (`git:https://host/owner/repo` with `version` as the tag, fetched with the `git` binary through `internal/exec`). spinup never runs code from a plugin: tier-1 plugins are data (templates and metadata).
- The plugin's hash is the SHA-256 of its normalized tree (sorted paths, LF-normalized text, modes), so a re-fetch gives the same hash. `spinup.lock` records `name`, `version`, `api`, and `sha256`.
- The allowlist is `.spinup/plugins.allow` (committed): one line per plugin, `<name> <source> <sha256>`. `plugin add` shows the source, the hash, and the files the plugin adds, asks for trust (or `--trust` without a TTY), and writes the allowlist line and the lock entry. Render, upgrade, and generators refuse a plugin that is missing from the allowlist or whose hash differs, with a `Try:` line. `plugin verify` re-fetches and checks every plugin (for CI).
- Cache: `<user cache dir>/spinup/plugins/<name>/<version>/`. Offline and missing from the cache: fail with a `Try:` line (critic: never render OLD without the plugin).
- Contract (pre-seed, `internal/plugin/contract.go`): `plugin.Resolve(ctx, Options) ([]Loaded, error)` returns the plugins of a project, verified against the allowlist and the lock. `Loaded` holds the metadata, the hash, and an `fs.FS` rooted at the pack. Unit M9.4 implements `Resolve` (fetch, cache, hash, allowlist, lock, and the `plugin` commands). Unit M9.5 uses it: plugin packs join `new`, render, `upgrade` (OLD renders with the locked plugin version, NEW with the declared one, so `plugin update` is an upgrade), and their declarative generators join `spinup generate`.
- Lock file entries of plugin files use the template prefix `plugin/<name>/`.
