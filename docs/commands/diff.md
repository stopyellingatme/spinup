# Compare application files

`spinup diff [path...]` compares tracked files with the locked template version.
The output uses `a/` for the template and `b/` for the disk.
The command lists missing files, tombstones, and rendered paths without lock entries.
Use `--json` for a JSON object with a `files` array.
Use a path argument to select a file or directory.
The command exits zero when differences exist.

Use `--dir <directory>` to select a project.
Use `--offline` to prevent template downloads.
If a template is unavailable, use `spinup cache warm`.

```console
$ spinup diff --help
...
```
