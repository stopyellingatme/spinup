# Generate and verify CI files

`spinup ci generate` renders the ci pack from the template version and answers in `spinup.lock`.
The command updates the managed files and their lock entries.
It requires the ci pack in the lock.

```console
$ spinup new todo --api-only --modules ci --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup ci generate --dry-run
...
$ spinup ci generate
Created 0, updated 0 files
$ spinup ci verify --json
...
```

Use `spinup ci services --json` to create a GitHub Actions service matrix.
The output is one compact JSON line.
Use `spinup ci services` to show a table.

Use `spinup ci test-service <name>` to test one declared service in Docker.
Use `--dry-run` to print the temporary Dockerfile without Docker.

```console
$ spinup g service ocr --lang rust
...
$ spinup ci services --json
{"include":[{"name":"ocr","lang":"rust"}]}
$ spinup ci test-service ocr --dry-run
FROM rust:1.99.1-alpine3.24@sha256:b1d5dd5a22a09404d3e798c0cf051b689077360374eb505fa5bb93c479693d87
WORKDIR /app
COPY . .
RUN cargo test --locked
```

Use `--dry-run` to list the file actions without changes.
Use `--diff` to display the differences without changes.
Select only one preview flag.
Use `--json` for a structured report.

If a managed file has user changes, generation stops before writes.
Save the changes before you use `--force` to replace the file.
The command preserves the other packs and the locked template version.
Use `--offline` to require local templates.

`spinup ci verify` checks workflow YAML and local action YAML under `.github/`.
It requires a complete commit SHA for each external action.
It requires top-level permissions in each workflow.
It rejects a head checkout with `pull_request_target`.
It also runs `actionlint` when the executable is on `PATH`.
Otherwise, the report includes a note about the omitted check.

Each finding produces one output line.
The command exits with code 1 when it finds a problem.
The JSON report contains `findings` and `notes` arrays.
Each finding contains `path`, `line`, and `message` fields.
