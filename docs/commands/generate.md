# Generate application files

Use `spinup generate <kind> [args...]` to generate files inside an application.
The alias is `spinup g`.
Each registered generator supplies its arguments, flags, and help.

```console
$ spinup g --help
Generate application files
...
$ spinup g
Available generators:
...
Error: Cannot select a generator
  a terminal is required for the picker
  Try: spinup g <kind> --help
[exit 2]
$ spinup g nope
Error: Cannot generate files
  generator "nope" is not registered
  Try: spinup g <kind> --help
[exit 2]
```

In a terminal, `spinup g` displays the registered kinds and their descriptions.
Select a kind, then enter its arguments and flags on one line.
The picker separates arguments at spaces.
For arguments that contain spaces, use the explicit command with shell quoting.
Without a terminal, the command lists the kinds and returns an error.
Use the explicit command to avoid the picker.

| Flag | Purpose |
| --- | --- |
| `--dry-run` | List each path, action, and class without writing. |
| `--diff` | Print unified diffs without writing. |
| `--force` | Permit changes to files with different content. |
| `--json` | Print the files, ordered post-steps, and suggested commands as JSON. |
| `--dir`, `-C <directory>` | Find the application from this directory. |
| `--non-interactive` | Disable the picker. |
| `--help`, `-h` | Show command help. |

Previews enforce the same overwrite rules as a normal run.
Identical content requires no write.
Without `--force`, different content causes an error that lists the affected files.
An exception permits changes to a managed file when the lock records a template that starts with `gen/`.

The runner records each written file in `spinup.lock`.
Each record contains the class, generator template, hash, and mode.
Requested post-steps run once each, in this order: `openapi`, `sqlc`, `gofmt`.
The tools run inside `api/`.
The `gofmt` step formats the planned Go files.

If a tool fails, the written files and their lock records remain available.
The error includes the tool output and a command for manual execution.
After a successful run, the runner prints the file counts and the generator’s `Next:` commands.
JSON output uses the fields `files`, `post_steps`, and `next`.
Previews with `--json` use the same JSON structure.
