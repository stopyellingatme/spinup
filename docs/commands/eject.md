# Take ownership of a file

`spinup eject <path>` changes a managed file to scaffold ownership in `spinup.lock`.
The command preserves the file content.
Future checks permit edits to this file.
The command rejects scaffold files, seed files, and paths without lock entries.
Use `--dry-run` to validate the change without writes.

Use `--dir <directory>` to select a project.
Use `--offline` to prevent template downloads.
If a template is unavailable, use `spinup cache warm`.

```console
$ spinup eject --help
...
```
