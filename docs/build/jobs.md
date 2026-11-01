---
title: Background jobs
parent: Build your app
nav_order: 8
---

# Background jobs

A background job is work that your API starts but does not wait for.
Examples are sending an email, building a report, or calling a slow service.
The handler inserts a job and answers at once. A separate worker process runs the job later.
Jobs use [River](https://riverqueue.com) and your PostgreSQL database, so you need no extra server.

Create an app with the `jobs` module, then add a job:

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

The job code lives in `api/internal/jobs/send_digest.go`.
The generated function only logs a line. Replace its body with your work.

## How it works

1. Your API inserts a row into the River tables of your database, with the job arguments as JSON.
2. The worker (`api/cmd/worker`) reads new rows, and runs the function that is registered for the job kind.
3. If the function returns an error, River keeps the job and tries again later.

The API and the worker are two programs that share one database.
Because the job is a database row, you can insert it in the same transaction as your data.
The worker sees the job only after that transaction commits.

## Define a job

Open the generated file. A job has three parts:

```go
// The arguments. River stores them as JSON, so use exported fields with json tags.
type SendDigestArgs struct {
	UserID int64 `json:"user_id"`
}

// The kind. Never change it after jobs of this kind exist.
func (SendDigestArgs) Kind() string { return "send_digest" }

// The work. Return an error to retry the job.
func SendDigest(d Deps) func(ctx context.Context, job jobs.Job[SendDigestArgs]) error {
	return func(ctx context.Context, job jobs.Job[SendDigestArgs]) error {
		slog.InfoContext(ctx, "job send_digest", "job_id", job.ID, "attempt", job.Attempt)
		// Use d.DB and job.Args.UserID here.
		return nil
	}
}
```

`Deps` in `api/internal/jobs/deps.go` holds what your jobs need, such as the database pool or a service client.
Add a field there, and set it where `api/cmd/worker/main.go` builds `jobsapp.Deps`.
The name can be snake, kebab, or camel case. `send_digest`, `send-digest`, and `SendDigest` make the same job.

The generator also writes `register_gen.go`.
It registers every job in the package, so you never edit it. It is rewritten on each run.

## Insert a job from a handler

The API needs an insert-only client.
Create it once in `api/cmd/api/main.go`, and pass it to your handlers:

```go
jobsClient, err := jobs.New(pool) // jobs is github.com/stopyellingatme/spinup/kit/jobs
if err != nil {
	return err
}
```

Then insert a job in a handler:

```go
id, err := jobsClient.Insert(ctx, jobsapp.SendDigestArgs{UserID: user.ID})
```

To insert the job together with your data, use `InsertTx` with the same transaction:

```go
tx, err := pool.Begin(ctx)
// ... write your rows with tx ...
_, err = jobsClient.InsertTx(ctx, tx, jobsapp.SendDigestArgs{UserID: user.ID})
err = tx.Commit(ctx)
```

If the transaction rolls back, the job is never created.

Pass insert options after the arguments:

| Option | Effect | Example |
| --- | --- | --- |
| `jobs.Queue(name)` | Use a queue other than `default`. | `jobs.Queue("mail")` |
| `jobs.Priority(n)` | 1 is the highest and the default, 4 is the lowest. | `jobs.Priority(3)` |
| `jobs.RunAt(t)` | Run at time `t` or later. | `jobs.RunAt(time.Now().Add(time.Hour))` |
| `jobs.MaxAttempts(n)` | Give up after `n` tries. | `jobs.MaxAttempts(5)` |
| `jobs.UniqueByArgs()` | Skip the insert if the same kind with the same arguments waits or runs. | `jobs.UniqueByArgs()` |

## Run a job on a schedule

Call `Periodic` in `newWorker` of `api/cmd/worker/main.go`.
The interval is a `time.Duration`. There are no cron expressions.

```go
w.Periodic(time.Hour, jobsapp.SendDigestArgs{})
```

Add the call before `w.Start`.
Only one worker process inserts periodic jobs, so more worker processes do not give more jobs.
For a fixed time of day, run the job every hour and check the time inside the function.

## Retries

A job that returns an error is tried again, with a longer wait after each failure.
River's default is 25 attempts. After the last attempt, River discards the job.

| Setting | Where | Example |
| --- | --- | --- |
| One job | Insert option | `jobs.MaxAttempts(5)` |
| All jobs | Worker option | `jobs.NewWorker(pool, jobs.WithMaxAttempts(10))` |

The function gets `job.Attempt`, which starts at 1.
A job can run more than once, so write it to be safe to repeat.
For example, check whether the email went out before you send it again.

## Queues and limits

All jobs use the `default` queue, and a worker runs up to 10 of them at once.
Give slow or fragile work its own queue, so it cannot use all the workers.
In `newWorker`:

```go
jobs.NewWorker(pool, jobs.WithQueue(jobs.DefaultQueue, 10), jobs.WithQueue("ocr", 2))
```

Then insert with `jobs.Queue("ocr")`. At most two `ocr` jobs run at once in each worker process.
The mail module adds a `mail` queue the same way.
If you name any queue with `WithQueue`, name the `default` queue too, or the worker will not take `default` jobs.

## Run the worker

In development, `spinup dev` starts the worker with the API and restarts it when Go files change.
Use `--no-worker` to leave it out.

<!-- doctest: skip -->
```console
$ spinup dev
$ spinup dev --no-worker
```

To run it by hand, apply the migrations and start it:

<!-- doctest: skip -->
```console
$ spinup db migrate up
$ cd api && go run ./cmd/worker
```

In deploy, the generated `deploy/compose.yml` has a `worker` service that runs the same image as the API.
It gets the same database secret and the same OpenTelemetry settings.
Set the number of workers per environment in `deploy/environments/<env>.toml`:

```toml
[replicas]
worker = 2
```

The default is 1. More workers share the queue.
On SIGINT or SIGTERM the worker stops taking new jobs and waits for running jobs.
It waits `SHUTDOWN_TIMEOUT` (default 20 seconds) and then cancels them.
So make a long job check `ctx.Done()`.

## See failed jobs

The worker logs each failed attempt. In development, the log appears in the `spinup dev` output.

To see the state of every job, query the `river_job` table.
A job that failed and waits for its next try has the state `retryable`.
A job that ran out of attempts has the state `discarded`.

<!-- doctest: skip -->
```console
$ psql "$DATABASE_URL" -c "select id, kind, state, attempt, errors from river_job where state in ('retryable', 'discarded') order by id desc limit 5"
```

To run a failed job again, insert a new job with the same arguments.

## Migrations

River keeps its own tables in your database.
`spinup db migrate up` applies your migrations first, then the River migrations.
`spinup db migrate down` does not roll River back. Use the app command for that:

<!-- doctest: skip -->
```console
$ cd api && go run ./cmd/api jobs-migrate status
$ cd api && go run ./cmd/api jobs-migrate down
```

## Test a job

The generated test `send_digest_test.go` runs your function against a test database with the kit `testkit` package.
Run it with `go test ./internal/jobs/...` in `api/`.
It needs `DATABASE_URL`.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `the app has no jobs module` | The app was made without `jobs`. | Run `spinup module add jobs`. |
| `jobs: insert a "..." job` with a missing table | The River migrations are not applied. | Run `spinup db migrate up`. |
| Jobs stay `available` | No worker runs, or the job is on a queue the worker does not take. | Start the worker. Check the queue names. |
| The worker logs `no job is registered` | The `jobs` package has no job yet. | Run `spinup g job <name>`. |
| A job is cancelled at shutdown | It ran longer than `SHUTDOWN_TIMEOUT`. | Make it check `ctx.Done()`, or raise the timeout. |

## See also

- [Generate a job](../commands/generate-job.md) and the [flag reference](../site/commands/generate-job.md)
- [Sidecar services](../services.md#hand-slow-work-to-a-job) for slow calls from a job
- [Mail](../mail.md), which sends through the `mail` queue
- [Audit trail](../audit.md) for the actor of a job
