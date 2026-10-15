# Operate the database

`spinup db` prints help for database operations.
`spinup db migrate [up|down|status]` runs migrations through the generated app.
The default operation is `up`.
The CLI runs `go run ./cmd/api migrate <operation>` in `api/`.
The command streams both output streams and returns the process exit status.

The target database always comes from `DATABASE_URL` in the environment or `../.env`.
The app reads these values through the kit.
The CLI passes no secrets.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup db migrate --dry-run
go run ./cmd/api migrate up
$ spinup db migrate status --dry-run
go run ./cmd/api migrate status
$ spinup db migrate down
Error: This rolls back the last migration and can lose data
  down requires explicit acknowledgement
  Try: spinup db migrate down --i-know
[exit 1]
```

| Flag | Purpose |
| --- | --- |
| `--i-know` | Acknowledge that `down` can lose data. Available on `db migrate`. |
| `--env <name>` | Resolve an environment from `spinup.toml`. Check its protection before `down`. |
| `--dir`, `-C <directory>` | Find the project root from this directory. |
| `--dry-run` | Print the migration command without execution. Guards still apply. |
| `--yes`, `-y` | Confirm a protected environment only when `CI` is set. |
| `--non-interactive` | Disable prompts. Protected operations fail without confirmation. |
| `--json` | Format CLI errors as JSON. App output retains its format. |
| `--verbose` | Enable extra diagnostics. |
| `--no-color` | Disable color. |
| `--help`, `-h` | Print command help. |

In M1, `--env` selects only the protection guard.
It does not replace `DATABASE_URL`.
Remote targets arrive with deploy.

If `spinup.toml` declares environments, the CLI always resolves the environment.
Without `--env`, it uses `default_env` or the only environment.
If the environment is ambiguous, pass `--env <name>`.
If no environments exist, omit `--env` to use the local database without an environment guard.
If no environments exist, an explicit `--env` produces an error.

For a protected environment, type the environment name in a terminal.
For automation, set `CI=true` and pass `--yes`.
The `down` operation also requires `--i-know`.
A dry run does not bypass either guard.

A missing project, unresolved environment, or missing Go executable produces an error with a `Try:` line.
A failed app process returns its exit status.
Inspect its output before another migration attempt.

<!-- doctest: requires-env DATABASE_URL -->
```console
$ spinup db migrate up
...
```
