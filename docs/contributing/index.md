---
title: Contributing
nav_order: 10
has_children: true
has_toc: false
---

# Contributing

These pages are for people who work on spinup itself.
If you want to build an app with spinup, start with [Get started](../get-started/index.md).

Build, test, and check the code with `make`. The Makefile is the only check contract.

```sh
make help
```

## Build and test

| Target | What it does |
| --- | --- |
| `make build` | Build every Go module. |
| `make test` | Run `go test -race`. Set `PKGS=./internal/x/...` to narrow the root module. |
| `make lint` | Run `golangci-lint` in every module, offline. |
| `make golden` | Run the golden-tree tests. Set `PACK=<name>` to narrow. Set `UPDATE=1` to rewrite. |
| `make script` | Run the testscript files in `testdata/script/`. Set `RUN=<glob>` to narrow. |
| `make app-gate` | Generate apps, then build and test them. It needs Docker and the network. |
| `make doctest` | Run the `console` blocks in `docs/`. |
| `make vuln` | Run `govulncheck`. It needs the network. |
| `make tools-check` | Check the versions of the toolchain. |
| `make e2e` | Run the browser suite on a generated app. It needs `DATABASE_URL` and the Chromium browser. |
| `make dind` | Run the deploy suite on a containerized VM. It needs Docker. |
| `make install` | Build `bin/spinup` as a local alpha version, and keep its templates for `spinup upgrade` tests. Commit first. |

Example: run the tests of one package, then lint.

```sh
make test PKGS=./internal/upgrade/...
make lint
```

Every change must pass `make build lint`.
If you change a command or a feature, add or update its page under `docs/`, and run `make doctest`.

## Repository layout

| Path | What it holds |
| --- | --- |
| `cmd/spinup/` | The `spinup` binary. |
| `internal/cli/` | The cobra commands. This package wires flags and calls other packages. It holds no logic. |
| `internal/gen/` | The generators. A generator returns a plan and never writes to disk. |
| `internal/templates/packs/` | The template packs that make a new app. |
| `internal/upgrade/`, `internal/merge/` | The upgrade engine and the 3-way merge. |
| `internal/stackversions/stackversions.yaml` | The one source of tool and image versions. |
| `kit/` | The runtime library of a generated app. It has its own `go.mod`. |
| `plugin-sdk/` | The SDK for executable plugins. It has its own `go.mod`. |
| `testdata/` | Golden trees, testscript files, the frozen upgrade corpus, and the end-to-end suites. |
| `scripts/` | Release, bump, and live-test scripts. |
| `docs/` | These docs. `docs/site/` is generated, so never edit it. |

## Pages

| I want to ... | Read |
| --- | --- |
| Write or change a docs page | [Writing the docs](writing-docs.md) |
| Cut a release and manage the signing key | [Release spinup](release.md) |
| See what remains before v1.0 | [v1.0 checklist](v1-checklist.md) |
| Review a template change or run the upgrade matrix | [Upgrade matrix](upgrade-matrix.md) |
| Refresh the pins of the sidecar templates | [Bump the sidecar pins](stack-bumps.md) |
| Run the deploy suite on a containerized VM | [Docker-in-Docker deploy suite](dind.md) |
| Run the disaster-recovery drill | [Disaster-recovery drill](dr-drill.md) |
| Run the browser tests and the trace check | [Browser end-to-end suite](e2e.md) |
| Boot the monitoring stack and check its memory | [Monitoring stack boot test](monitoring-boot.md) |
| Run the live DigitalOcean test | [Run the DigitalOcean live test](do-live-test.md) |
| Register the DigitalOcean OAuth client or change the fake API | [DigitalOcean OAuth client](digitalocean-client.md) |
| Fix a script test that fails under load | [Script test isolation](script-test-isolation.md) |
