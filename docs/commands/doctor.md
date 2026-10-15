# Check local health

`spinup doctor` reports the git version, lock validity, missing managed files, and kit compatibility.
Use `--quick` to run the git check only.
Use `--json` for a JSON array of results.
Checks that require a project skip outside a project.
Failures produce exit 1.
Warnings do not cause a failure.

The kit check skips development builds and a local kit replacement.
The kit check reads `api/go.mod` through `go mod edit -json`.
The command does not change the module.

Use `--dir <directory>` to select a project.
Use `--offline` to prevent template downloads.
If a template is unavailable, use `spinup cache warm`.

```console
$ spinup doctor --help
...
```
