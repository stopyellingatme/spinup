# ADR 0001: Architecture

Status: accepted (2026-10-13)

## Context

We want our own Buffalo-style CLI that creates and operates a full stack. Buffalo failed mostly on
maintenance cost: it owned too many tools and gave generated apps no safe upgrade path.

## Decision

Bullets 1 to 3 are superseded by ADR 0019 (Rust rewrite).

- The CLI is Go (cobra, embedded templates), module `github.com/stopyellingatme/spinup`.
- Generated apps use a Go API (stdlib ServeMux, oapi-codegen, pgx, sqlc, goose, River), a SvelteKit web app,
  PostgreSQL, Docker Compose on a VM with Caddy, and OpenTelemetry with self-hosted Grafana LGTM.
- Upgrades use a hybrid model. Most runtime code lives in the versioned `kit` library. Scaffolded files are
  tracked in `spinup.lock` with a file class (`managed`, `scaffold`, `seed`) and updated by a 3-way merge
  (`git merge-file --diff3`) inside a git worktree.
- We own the glue (render, lock, merge orchestration, codemods, deployer, plugin host) and delegate every tool.

The full design is in `docs/design/architecture.md`. The critic resolutions in `docs/design/plan.md` override it.

## Consequences

Superseded by ADR 0019 (Rust rewrite).

- Upgrade safety is tested from M2 on with a frozen corpus of real generated apps.
- The kit and the CLI share minor versions (lockstep). Patch versions ship independently.
