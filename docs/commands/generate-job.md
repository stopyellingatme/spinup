# Generate a background job

Run `spinup g job <name>` from the application root to add a background job.
The command also accepts the name `spinup generate job`.
The jobs run on [River](https://riverqueue.com) with the application database.

The app must have the `jobs` module.
Create the app with `--modules ci,jobs` to get it.
A command to add a module to an existing app arrives in a later spinup version.

<!-- doctest: only go -->
```console
$ spinup new todo --api-only --module example.com/todo --modules ci,jobs --no-git --non-interactive
...
$ cd todo
$ spinup g job send_digest --dry-run
create api/internal/jobs/register_gen.go (managed)
create api/internal/jobs/send_digest.go (scaffold)
create api/internal/jobs/send_digest_test.go (scaffold)
$ spinup g job send_digest
Created 3, updated 0 files
Next: cd api && go test ./internal/jobs/...
Next: cd api && go run ./cmd/worker
$ grep -h 'Kind\|jobs.Register(' api/internal/jobs/send_digest.go api/internal/jobs/register_gen.go
// Kind names the job kind in River. Do not change it after jobs exist.
func (SendDigestArgs) Kind() string { return "send_digest" }
	jobs.Register(w, SendDigest(d))
```

The name can be in snake case, kebab case, or camel case.
`send_digest`, `send-digest`, and `SendDigest` give the same job.
The job kind and the file name use snake case.

## The files

The `jobs` module adds these files:

| File | Class | Contents |
| --- | --- | --- |
| `api/cmd/worker/main.go` | scaffold | The worker process. It opens the database, registers the jobs, and stops gracefully on a signal. |
| `api/internal/jobs/deps.go` | scaffold | The `Deps` struct with the dependencies of the jobs. |
| `api/internal/jobs/register.go` | managed | The `Register` function that `cmd/worker` calls. |

Each `spinup g job <name>` adds these files:

| File | Class | Contents |
| --- | --- | --- |
| `api/internal/jobs/<name>.go` | scaffold | The `<Name>Args` type with its `Kind` method, and the worker function `<Name>(d Deps)`. |
| `api/internal/jobs/<name>_test.go` | scaffold | A test that runs the worker function on a test database (kit `testkit`). |
| `api/internal/jobs/register_gen.go` | managed | The registration of every job in the package, sorted by name. |

The generator owns `register_gen.go`.
It rewrites the file on each run, without `--force`.
It reads every Go file in `api/internal/jobs/` and registers each `<Name>Args` type that has a `Kind` method and a `<Name>` function.
Do not edit `register_gen.go`.

Add the job arguments as exported fields with `json` tags.
River stores them as JSON.
Do not change the `Kind` value after jobs of that kind exist.

## Insert a job

Use an insert-only client from kit `jobs` in the API process:

```go
client, err := jobs.New(pool)
id, err := client.Insert(ctx, jobsapp.SendDigestArgs{}, jobs.Queue("default"), jobs.UniqueByArgs())
```

`InsertTx` inserts a job in a `pgx.Tx`.
The worker sees the job only after the transaction commits.
The insert options are `Queue`, `Priority`, `RunAt`, `MaxAttempts`, and `UniqueByArgs`.

Use `w.Periodic(time.Hour, jobsapp.SendDigestArgs{})` in `cmd/worker/main.go` to insert a job at an interval.
Only one worker process inserts periodic jobs, so more workers do not give more jobs.

## Migrations

River keeps its tables in the application database.
`spinup db migrate up` applies the app migrations first and then the River migrations.
`spinup db migrate down` does not roll back River.
Use the explicit command for that:

```sh
cd api && go run ./cmd/api jobs-migrate status
cd api && go run ./cmd/api jobs-migrate down
```

## Run the worker

Start the database, apply the migrations, and run the worker:

```sh
spinup db migrate up
cd api && go run ./cmd/worker
```

The worker stops taking jobs on SIGINT or SIGTERM.
It waits for the running jobs until `SHUTDOWN_TIMEOUT` (default 20s) ends, and then cancels them.

## Errors

If the app has no `jobs` module, the command fails:

<!-- doctest: only go -->
```console
$ spinup new plain --api-only --module example.com/plain --no-git --non-interactive
...
$ cd plain
$ spinup g job send_digest
Error: generator "job" failed: cannot generate a job: the app has no jobs module (the jobs pack is not in spinup.lock)
  Try: spinup module add jobs
[exit 1]
```

<!-- doctest: only rust -->
```console
$ spinup new plain --api-only --module example.com/plain --no-git --non-interactive
...
$ cd plain
$ spinup g job send_digest
Error: generator "job" failed: cannot generate a job: the app has no jobs module
  Try: spinup module add jobs
[exit 1]
```

If a job file exists with different content, the command fails and lists the file.
Pass `--force` to replace it.
Use `--dry-run` or `--diff` to preview without writing or running tools.
Use `--json` for structured results.

## Rust API

In a Rust API, `spinup g job <name>` writes the scaffold file `api/src/jobs/<name>.rs`.
It also rewrites the managed module list `api/src/jobs/mod.rs`, with one sorted line for each job.
The generator finds the existing jobs by their file names in `api/src/jobs/`.
It does not parse the Rust code.
