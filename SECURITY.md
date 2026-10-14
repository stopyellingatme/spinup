# Security policy

## Report a vulnerability

Do not open a public issue. Use GitHub private vulnerability reporting on this repository
(Security tab, "Report a vulnerability").

## Supported versions

Security fixes go to the last two minor versions of the CLI and the kit.

## Scope

- The `spinup` CLI and the `kit` runtime library.
- The templates that `spinup` generates (Compose files, Caddy config, CI workflows).
- Tier-2 plugins run with the full privileges of the user. A malicious plugin is not a spinup vulnerability.
