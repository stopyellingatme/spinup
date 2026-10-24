# spinup maintenance

Keep a deployed VM healthy

## Use

```text
spinup maintenance
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--confirm-env` | `` | type the protected environment name without a terminal |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Subcommands

- [`spinup maintenance certs`](maintenance-certs.md) — Check the public certificate expiry
- [`spinup maintenance patch`](maintenance-patch.md) — Install available package updates
- [`spinup maintenance pg-major-upgrade`](maintenance-pg-major-upgrade.md) — Upgrade the PostgreSQL major with a verified dump and retained old volume
- [`spinup maintenance pg-minor-upgrade`](maintenance-pg-minor-upgrade.md) — Upgrade PostgreSQL to the pinned minor after a backup
- [`spinup maintenance pg-upgrade-in-place`](maintenance-pg-upgrade-in-place.md) — Upgrade PostgreSQL with pg_upgrade --link and retain the old directory
- [`spinup maintenance prune`](maintenance-prune.md) — Remove unused app images and build cache
- [`spinup maintenance vacuum`](maintenance-vacuum.md) — Vacuum the production database
- [`spinup maintenance vuln-scan`](maintenance-vuln-scan.md) — Scan running images with Trivy
- [`spinup maintenance window`](maintenance-window.md) — Show the maintenance window
