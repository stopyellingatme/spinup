# Changelog

All notable changes to spinup appear in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- M0: Establish the Go CLI, kit, manifests, templates, and checks for deterministic output.
- M1: Generate Go APIs and PostgreSQL migrations. Add build commands, database commands, and documentation checks.
- M2: Track generated files with a lock file. Add upgrades with a three-way merge and template caches.
- M3: Generate resources, models, handlers, migrations, jobs, and OpenAPI contracts.
- M4: Generate SvelteKit applications, authentication, pages, components, and clients. Add the development loop and browser checks.
- M5: Deploy through SSH with blue/green releases. Add encrypted secrets, server commands, rollback, and backups.
- M6: Add OpenTelemetry, metrics, logs, traces, dashboards, alerts, and monitoring commands.
- M7: Verify and restore backups. Add PostgreSQL maintenance, remote checks, dependency updates, and recovery drills.
- M8: Generate GitHub workflows and browser checks. Add release archives, signatures, SBOMs, provenance, an installer, Homebrew, and the CI image.

### Breaking

- No release is declared by this section.

### Upgrade steps

- Review `spinup upgrade --plan` before each upgrade.
- Commit the generated files and the lock file after a successful upgrade.
