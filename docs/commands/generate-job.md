---
title: generate job — add a background job
parent: Command guides
---

# generate job — add a background job

A background job is work that runs outside a web request, such as sending a digest email.
`spinup g job <name>` adds the job code, a test, and its registration.
The jobs run on [River](https://riverqueue.com) with the database of your app, so you need no extra service.

The app needs the `jobs` module.
Create the app with `--modules ci,jobs`, or add it later with `spinup module add jobs`.

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
```

Edit `api/internal/jobs/send_digest.go` to do the work.
Then insert a job from your API and run the worker.

## Name the job

The name can be in snake case, kebab case, or camel case.
`send_digest`, `send-digest`, and `SendDigest` give the same job.
The job kind and the file name use snake case, and the Go names use camel case.
A name with spaces or a leading digit fails with a `Try:` line.

## What it writes

| File | Class | What it is |
| --- | --- | --- |
| `api/internal/jobs/<name>.go` | scaffold | The `<Name>Args` type, its `Kind` method, and the function that does the work. |
| `api/internal/jobs/<name>_test.go` | scaffold | A test that runs the job on a test database. |
| `api/internal/jobs/register_gen.go` | managed | The list of all jobs, sorted by name. The generator rewrites it on every run without `--force`, so do not edit it. |

The generator builds `register_gen.go` from every Go file in `api/internal/jobs/`.
It lists each `<Name>Args` type that has a `Kind` method and a `<Name>` function.
The `jobs` module itself adds `api/cmd/worker/main.go` (the worker process), `api/internal/jobs/deps.go` (what the jobs may use, such as the database pool), and `register.go`.

The job file looks like this:

```go
type SendDigestArgs struct{}

func (SendDigestArgs) Kind() string { return "send_digest" }

func SendDigest(d Deps) func(ctx context.Context, job jobs.Job[SendDigestArgs]) error {
	return func(ctx context.Context, job jobs.Job[SendDigestArgs]) error {
		slog.InfoContext(ctx, "job send_digest", "job_id", job.ID, "attempt", job.Attempt)
		return nil
	}
}
```

Add the job arguments as exported fields of `SendDigestArgs` with `json` tags, because River stores them as JSON.
Return an error from the function to retry the job later.
Do not change the `Kind` value after jobs of that kind exist, or River cannot find their worker.

## Insert a job

Insert jobs from your API with the client in the kit:

```go
client, err := jobs.New(pool)
id, err := client.Insert(ctx, jobsapp.SendDigestArgs{}, jobs.Queue("default"), jobs.UniqueByArgs())
```

| Option | What it does | Example |
| --- | --- | --- |
| `jobs.Queue(name)` | Pick the queue. | `jobs.Queue("default")` |
| `jobs.Priority(n)` | Run it before lower priorities. | `jobs.Priority(1)` |
| `jobs.RunAt(t)` | Run it at a later time. | `jobs.RunAt(time.Now().Add(time.Hour))` |
| `jobs.MaxAttempts(n)` | Limit the retries. | `jobs.MaxAttempts(5)` |
| `jobs.UniqueByArgs()` | Skip the insert if the same job waits. | `jobs.UniqueByArgs()` |

Use `client.InsertTx(ctx, tx, args)` to insert inside a transaction.
The worker sees the job only after the transaction commits.

## Run a job on a schedule

To run a job at an interval, add `Periodic` in `newWorker` of `api/cmd/worker/main.go`:

```go
jobsapp.Register(w, jobsapp.Deps{DB: pool})
w.Periodic(time.Hour, jobsapp.SendDigestArgs{})
```

Only one worker process inserts the periodic jobs, so more workers do not give you more jobs.

## Run the worker

Apply the migrations, then start the worker:

<!-- doctest: skip -->
```console
$ spinup db migrate up
$ cd api && go run ./cmd/worker
```

`spinup dev` also starts the worker.
On SIGINT or SIGTERM the worker stops taking jobs.
It waits for the running jobs until `SHUTDOWN_TIMEOUT` (default 20 seconds) ends, and then cancels them.

## River migrations

River keeps its tables in the database of your app.
`spinup db migrate up` applies your migrations first and the River migrations after.
`spinup db migrate down` does not roll back River.
Use the app command for that:

<!-- doctest: skip -->
```console
$ cd api && go run ./cmd/api jobs-migrate status
$ cd api && go run ./cmd/api jobs-migrate down
```

## Troubleshooting

If the app has no `jobs` module, the command fails and names the fix:

```console
$ spinup new plain --api-only --module example.com/plain --no-git --non-interactive
...
$ cd plain
$ spinup g job send_digest
Error: generator "job" failed: cannot generate a job: the app has no jobs module (the jobs pack is not in spinup.lock)
  Try: spinup module add jobs
[exit 1]
```

| Symptom | Cause and fix |
| --- | --- |
| A job file exists with different content | The command lists it and stops. Pass `--force` to replace it, or `--diff` to compare. |
| A job never runs | Check that the worker runs. Check that the job is in `register_gen.go`: it needs a `<Name>Args` type with a `Kind` method and a `<Name>` function. |
| A job runs twice | A job that fails is retried. Make the work safe to repeat. |

## See also

- [Background jobs](../build/jobs.md): the guide to jobs in your app
- [generate](generate.md): preview and overwrite rules
- [module](module.md): `spinup module add jobs`
- Flag reference: [`generate job`](../site/commands/generate-job.md)
