# Run application tests

`spinup test` runs the API tests in the `api/` directory.
The command streams the output and returns the test exit code.
The `--api` target is the default.

In a Go API, the command runs `go test ./...`.

<!-- doctest: only go -->
```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup test --dry-run
go test ./...
$ spinup test --api --run Todo --race --cover --dry-run
go test -run Todo -race -cover ./...
```

Use `--run <regexp>` to select tests.
Use `--race` to enable the race detector.
Use `--cover` to report coverage.
Use `--dir <directory>` to select a project.
Use `--dry-run` to print the command without execution.

## Rust API

In a Rust API, the command runs `cargo nextest run --locked` in `api/`.
`--run <filter>` gives nextest a test name filter.
`--cover` runs `cargo llvm-cov nextest --locked` instead.
Rust has no race detector, so the command has no `--race` flag.

<!-- doctest: only rust -->
```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup test --dry-run
cargo nextest run --locked
$ spinup test --api --run todo --cover --dry-run
cargo llvm-cov nextest --locked todo
$ spinup test --web
Error: cannot run web tests: this target is unavailable
  Try: use --api until M4 adds web tests
[exit 1]
```

Install the test tools with `cargo install cargo-nextest --locked` and `cargo install cargo-llvm-cov --locked`.
If a tool is missing, the error gives the install command.
Generated tests that need PostgreSQL read `DATABASE_URL`.
Without it, each of these tests prints `skip: DATABASE_URL is not set` and passes.
When `CI` is set, these tests fail instead, with a `Try:` line.

## Unavailable targets

The `--web`, `--e2e`, and `--contract` targets require M4.
Each unavailable target produces an error with a `Try:` line.
