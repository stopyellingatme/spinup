# Check application drift

`spinup check` exits 4 for managed drift, conflict markers, an invalid lock, missing managed files, or newer loadable templates.
Each problem includes a correction.
Use `--allow-behind` to permit newer templates.
Use `--json` for a JSON object with a `problems` array.
The command exits zero when the project is clean.
Scaffold edits are permitted if they contain no conflict markers.

Use `--dir <directory>` to select a project.
Use `--offline` to prevent template downloads.
If a template is unavailable, use `spinup cache warm`.

```console
$ spinup check --help
...
```
