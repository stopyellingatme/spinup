# Contributing to spinup

This page is the entry point for a change to spinup itself.
If you want to build an app with spinup, read the [documentation](https://stopyellingatme.github.io/spinup/).
spinup has one maintainer, so a small pull request gets a faster review.

## Before you start

If you fix a small bug or a docs error, open a pull request.

If your change is larger, open an issue first. Wait for an answer before you write code. Examples are a new command, a new module, a kit API change, or a template pack change.

spinup does not accept CGO, an ORM, a custom template language, or telemetry in the CLI.

If you found a vulnerability, follow [SECURITY.md](SECURITY.md). Do not open an issue.

## Repository layout

| Path | What it holds |
| --- | --- |
| `cmd/spinup/`, `internal/` | The `spinup` binary and the packages behind it. |
| `internal/templates/packs/` | The template packs that make a new app. |
| `kit/` | The runtime library of a generated app. |
| `plugin-sdk/` | The SDK for executable plugins. |
| `docs/` | The documentation. The folder `docs/site/` is generated, so do not edit it. |
| `docs/adr/`, `docs/design/` | The decisions and the design of spinup. |
| `testdata/script/` | The testscript files for the CLI. |
| `testdata/golden/` | The golden trees of generated apps. |

The file `go.work` joins the three Go modules: the root module, `kit/`, and `plugin-sdk/`.
The [full table](https://stopyellingatme.github.io/spinup/contributing/) lists every folder.

## Prepare your machine

Install these tools.

| Tool | Version | Note |
| --- | --- | --- |
| Go | 1.27.2 or later | The Makefile sets `GOTOOLCHAIN=local`, so Go never downloads another toolchain. |
| git | 2.50 or later | `make tools-check` also needs `git merge-file --diff-algorithm`. |
| golangci-lint | 2.14.0 or later | `make lint` runs it. |
| Node | 24 or later | Node 24 includes corepack. |
| Docker | 27 or later | The kit tests start PostgreSQL in Docker. |

Do not install pnpm. The script `scripts/bin/pnpm` runs the pinned version through corepack, and the Makefile puts `scripts/bin` on `PATH`.

Run this command to check the tools:

```sh
make tools-check
```

The command prints one line for each tool. If any tool is missing or too old, the command exits with 1.

For most changes, you can ignore a missing `goreleaser`, `syft`, `cosign`, `promtool`, `sops`, `age`, or `actionlint`. The command still exits with 1.

The file `CLAUDE.md` names Homebrew paths. The Makefile adds `/opt/homebrew/opt/node@24/bin` to the start of `PATH`.
On another system, that folder does not exist, so `PATH` ignores it. Put Node 24 on `PATH` yourself.

## First build

Build every module, then build the binary:

```sh
make build
go build -o bin/spinup ./cmd/spinup
```

Some tests build a generated app with `GOPROXY=off` or run `pnpm install --offline`.
Those tests need the Go modules and the pnpm packages of the generated apps in your local caches.
If such a test fails because a module or a package is missing, fill the caches once:

```sh
scripts/ci/warm-cache.sh
```

The script downloads from the network and does not change the repository.
CI runs it before the tests. You can pass `go` or `node` to fill one cache.

## Checks before a pull request

Run these checks. Each pull request must pass the first one.

```sh
make build lint
make test PKGS=./internal/upgrade/...
make doctest
make golden PACK=api
make script RUN='<glob>'
```

- Replace `./internal/upgrade/...` with the package that you changed. The target also runs the kit and plugin SDK tests.
- If you change a docs page, run `make doctest`.
- If you change a template pack, run `make golden`. Replace `api` with the name of the pack.
- If you change a command, run `make script`. Replace `<glob>` with the name of a script in `testdata/script/`.

The [full table of targets](https://stopyellingatme.github.io/spinup/contributing/) lists every target.

## Rules for a change

These rules apply to each change.

- Register a command, a doctor check, a generator, or a deploy hook with `Register(...)` in `init()`. Do not edit a central list.
- Keep `internal/cli` thin. It holds flags and wiring only.
- Make a generator return a plan. A generator never writes to disk. Only the apply step writes.
- Run each subprocess through `internal/exec`.
- Give each template file the class `managed`, `scaffold`, or `seed` in its `pack.toml`. If you are not sure, use `managed`.
- Make each user-facing error say what failed and why. End it with a `Try:` line.
- Give each prompt a flag. If the CLI has no terminal, it fails and names the missing flag.
- Make rendered output the same for the same input. Do not use the clock, random values, or map order.

## Files that need the maintainer

Do not edit these files:

- `go.mod`, `go.sum`, `go.work`, `go.work.sum`, `kit/go.mod`, `kit/go.sum`, and `plugin-sdk/go.mod`
- The lockfiles of the template packs
- `internal/stackversions/stackversions.yaml`
- `Makefile` and `scripts/tools-check.sh`
- `cmd/spinup/wire_gen.go`
- The contract files, for example `internal/gen/contract.go`
- Vendored files
- `CLAUDE.md`

The section "Shared files" of [CLAUDE.md](CLAUDE.md) is the full list. If you are not sure, ask in an issue.

If you need a new dependency or a new pin, open an issue. Say which dependency or pin you need and why.
Do not run `go get`, `go mod tidy`, or `pnpm add`.

## Template pack changes

If you change a file under `internal/templates/packs/`, update the pack digest:

```sh
SPINUP_GOLDEN_UPDATE=1 go test ./internal/templates -run PacksDigest
```

The command writes `REVIEW` in the digest file. Replace `REVIEW` with the ID of the codemod that migrates existing apps.
If existing apps need no codemod, write `none`. Say why in the pull request.

Read the [upgrade matrix guide](https://stopyellingatme.github.io/spinup/contributing/upgrade-matrix.html) first.

## Docs

Each command or feature has a page under `docs/` with a runnable code block. The [writing guide](https://stopyellingatme.github.io/spinup/contributing/writing-docs.html) has the rules.
Add a line to the section `Unreleased` in [CHANGELOG.md](CHANGELOG.md). Do not edit `docs/site/`.

## Tests that need Docker or the network

You do not need to run `make app-gate`, `make e2e`, `make dind`, or `make vuln`. CI or the maintainer runs them.
In the pull request, say which checks you did not run.

## Commit messages

Use the form `area: summary`. For example, `kit: add app.WithSPAOptions`.
Do not use the prefix `orch:`, because the agents of the maintainer use it.

## Pull requests

1. Fork the repository.
2. Make a branch from `main`.
3. Push the branch and open a pull request.

GitHub holds the CI run of a pull request from a fork until the maintainer approves it.
Six checks must pass: `lint`, `actionlint`, `test`, `windows`, `upgrade`, and `test-macos`.
The check `test-macos` starts only after the other five pass.
The code owner reviews each pull request. The maintainer merges with a merge commit or a squash.

## Changes made with an AI agent

spinup is built with AI agents, and you can use one.
You are the author of your change. Read each line of it. Run the checks. Be ready to explain the change.
In the pull request, say which parts an agent wrote.
The section "Rules for code" of [CLAUDE.md](CLAUDE.md) applies to you. Its lane sections do not.

## License of your contribution

You keep the copyright of your contribution.
When you open a pull request, you license it under the MIT license in [LICENSE](LICENSE).
You also agree that the [generated code permission](README.md#generated-code) applies to it.
spinup has no CLA and no sign-off line.

If you add third-party code, name its source and its license. Add it to [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Security and conduct

- Report a vulnerability as [SECURITY.md](SECURITY.md) describes.
- Follow the [code of conduct](CODE_OF_CONDUCT.md).
