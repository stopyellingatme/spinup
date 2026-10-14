# ADR 0004: Default backup backend

Status: accepted (2026-10-13)

## Decision

**pgBackRest stays the default backend.** `pgdump` is the second backend (M7). WAL-G is the third (M10).
All backends sit behind the `Backend` interface, so a later switch does not change the CLI surface.

## Rule (plan resolution 20)

If pgBackRest has no release in 90 days, make `pgdump` plus WAL-G the primary backends. The rule did not trigger.

## Evidence (GitHub API, 2026-10-13)

| Project | Latest release | Releases in 90 days | Commits in 90 days | Archived |
|---|---|---|---|---|
| pgbackrest/pgbackrest | release/2.59.3 (2026-10-04) | 4 (2.59.0 to 2.59.3) | 208, last 2026-10-13 | no |
| wal-g/wal-g | v3.0.9 (2026-08-20) | 1 | 241, last 2026-10-12 | no |

- pgBackRest had a 6-month gap with no release (2.58.0 on 2026-01-19 to 2.59.0 on 2026-07-20).
  The claim that the project was archived in April 2026 and revived in May 2026 could not be confirmed
  from the repository. Treat that claim as unverified.
- Packaging: the generated `Dockerfile.postgres` installs `pgbackrest` 2.59.3 from apt.postgresql.org
  into `postgres:18.6-trixie` (package `2.59.3-1.pgdg13+1`). Pin the Debian variant explicitly.
- Re-run this check before each minor release. `spinup doctor` can surface the backend version.
