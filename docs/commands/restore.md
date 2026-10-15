# Restore a template file

`spinup restore <path>` writes the file from the locked template version.
The command removes its tombstone and updates its hash and mode in `spinup.lock`.
If an existing file differs, use `--force` to replace it.
The command preserves an existing scaffold classification.
Use `--dry-run` to validate the change without writes.

Use `--dir <directory>` to select a project.
Use `--offline` to prevent template downloads.
If a template is unavailable, use `spinup cache warm`.

```console
$ spinup restore --help
...
```
