# Run application tests

`spinup test` runs `go test ./...` in the `api/` directory.
The command streams the output and returns the test exit code.
The `--api` target is the default.

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

The `--web`, `--e2e`, and `--contract` targets require M4.
Each unavailable target produces an error with a `Try:` line.
