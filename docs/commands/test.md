---
title: test — run the app tests
parent: Command guides
---

# test — run the app tests

`spinup test` runs the Go tests of your API. It streams the output and exits with the code of `go test`.
Run it before you commit, or let [Generated CI](../ci.md) run it for you.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup test --dry-run
go test ./...
```

`--dry-run` prints the command and does not run it. Leave it off to run the tests.
The command works from any folder inside the app, because it finds the project root. It then runs `go test` in `api/`.

## Run some of the tests

Run only the tests whose names match a regular expression with `--run`:

```console
$ spinup test --run Todo --dry-run
go test -run Todo ./...
```

Add the race detector with `--race`, and a coverage report with `--cover`:

```console
$ spinup test --run Todo --race --cover --dry-run
go test -run Todo -race -cover ./...
```

`--api` selects the API tests. It is the default, so you never need to type it.

Test an app in another folder with `--dir`:

```console
$ cd ..
$ spinup test --dir todo --dry-run
go test ./...
```

## Test the web app

`spinup test --web`, `--e2e`, and `--contract` are not available yet. Each one stops with an error and exit code 1:

```console
$ spinup test --web
...
[exit 1]
```

Run these tests with the web tools instead. [Test your app](../build/testing.md) shows each one.

<!-- doctest: skip -->
```console
$ cd web && pnpm test
$ cd web && pnpm test:e2e
```

## Flags

| Flag | What it does | Example |
| --- | --- | --- |
| `--run <regexp>` | Run the tests that match. | `spinup test --run Todo` |
| `--race` | Turn on the race detector. | `spinup test --race` |
| `--cover` | Report coverage. | `spinup test --cover` |
| `--api` | Run the API tests. This is the default. | `spinup test --api` |
| `--web`, `--e2e`, `--contract` | Not available yet. | `cd web && pnpm test` |
| `--dir <directory>`, `-C` | Test the app in this folder. | `spinup test -C ~/code/todo` |
| `--dry-run` | Print the command and do not run it. | `spinup test --dry-run` |

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `could not find spinup.toml` | You are outside an app. | Run the command in the app, or pass `--dir`. |
| `Application tests failed` | A test failed. The exit code is the one from `go test`. | Read the test output above the error. |
| A database test is skipped. | There is no `DATABASE_URL` and no Docker. | Set `DATABASE_URL`, or start Docker. |

## See also

- [Test your app](../build/testing.md) covers handler tests, database tests, and web tests.
- [`dev`](dev.md) starts the database that tests can use.
- Flag reference: [`test`](../site/commands/test.md)
