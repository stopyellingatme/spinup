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

### Changed

- Load test fixes: the database pool holds 20 connections by default (`db.DefaultMaxConns`), and `DATABASE_MAX_CONNS` changes the size. Login, registration, and password reset answer 503 with `Retry-After` when the password hash queue is full (`auth.WithHashLimits`). A login takes its queue place before its database work. One hash runs for each 10 CPUs (at least 1), so a login burst cannot starve the other routes.

- The resource list page costs less to render. The page renders one actions menu (`row-menu.svelte`) outside the rows. Each row has a plain button for the menu and inline icons. The row checkbox is a submit button that works without JavaScript. The toggle form keeps the page number. The page uses 69% less Node CPU in the load test.

### Fixed

- The web pack `app.css` defines the shadcn-svelte variants `data-open:`, `data-closed:`, `data-checked:`, `data-unchecked:`, `data-disabled:`, `data-active:`, `data-horizontal:`, and `data-vertical:`. Before this change, the checkbox had no checked fill, and the menus had no open and close animation.

### Breaking

- No release is declared by this section.
- `db.WithMaxConns` takes an `int`, not an `int32`.

### Upgrade steps

- Review `spinup upgrade --plan` before each upgrade.
- Commit the generated files and the lock file after a successful upgrade.
