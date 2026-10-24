# R0.8 Reference app C: jobs and OTel

Issue: #16. Spike code: `spikes-rs/r0-08-ref-jobs-otel/`.

## Question

Can the Rust kit replace River with its own job queue on sqlx, and keep at-least-once delivery when the worker dies? Can `tracing`, `tracing-opentelemetry`, and the OTLP HTTP exporter give the metric names that the monitoring pack queries, so that the Grafana RED dashboard shows data for a Rust API?

## Method

I copied the R0.6 workspace (`kit`, `todo-api`, `.cargo/config.toml` with `SQLX_OFFLINE = "true"`, and `.sqlx/`). I did not copy `genapp` and `compare`. Then I added two kit features. Both are in the default feature set:

- `jobs` (`kit/src/jobs.rs`): the own queue. The kit migration `0001_job_queue` replaces the R0.6 stand-in. It has the River schema: the `kit_job_state` enum, one `kit_job` table, an unlogged `kit_leader` table, and the `kit_insert` NOTIFY trigger.
  - Insert: one statement. `unique_by_args` writes `unique_key = sha256(kind || ':' || args::jsonb::text)`. A partial unique index allows one key in the states `available`, `pending`, `retryable`, `running`, and `scheduled`. `ON CONFLICT DO NOTHING` then returns the ID of the job that exists. `insert_tx` inserts in the transaction of the caller.
  - Fetch: `UPDATE ... FROM (SELECT ... FOR UPDATE SKIP LOCKED LIMIT <free slots>)`. It sets `running`, increments `attempt`, and appends the worker ID to `attempted_by`.
  - Wake-up: a `PgListener` on `kit_insert` wakes the fetch loop. The loop also polls every `poll_interval`. After a lost listener connection, the loop fetches at once.
  - Retry: a failed attempt goes to `retryable` with `scheduled_at = now() + backoff(attempt)`. The error entry records `attempt`, `at`, `error`, and `retry_at`. After `max_attempts`, the job goes to `discarded`. The default backoff is River's: attempt⁴ s, ±10 % jitter, at most 24 h. A panic in a handler is an error.
  - Leader: an upsert on `kit_leader` with a TTL. The leader runs the rescuer and inserts the periodic jobs as unique jobs. The rescuer moves a job that stays `running` longer than `rescue_after` back to `retryable` (with the backoff) or to `discarded`.
  - Stop: the worker stops fetching, waits `stop_timeout` for the running jobs, cancels the others, and gives up the lead. The running jobs and the background tasks live in `JoinSet`s that the `run` future owns. When the future is dropped (an aborted task, a crash), every job of that worker stops.
  - A state change of a running job checks `state = 'running' AND attempt = <the attempt>`. So a job goes to `completed` once.
- `telemetry` (`kit/src/telemetry.rs`): `init` starts the JSON log. With `OTEL_EXPORTER_OTLP_ENDPOINT`, it also starts traces, metrics, and logs over OTLP HTTP protobuf (`/v1/traces`, `/v1/metrics`, `/v1/logs`). It sets the global providers and the W3C trace context propagator. The tracing subscriber has the JSON log layer, the `tracing-opentelemetry` layer, and the `opentelemetry-appender-tracing` bridge. The bridge drops the events of `hyper`, `reqwest`, `h2`, and the OTel crates, so an export does not log itself.
  - The HTTP layer (`http_layer`, in `kit::app::router` between the request ID and the access log) makes one server span for each request. The span is a child of the `traceparent` header, and its name is `<METHOD> <route pattern>`. The layer records the histogram `http.server.request.duration` (unit `s`, the semantic convention bounds 5 ms to 10 s) with `http.request.method`, `http.route` (the axum `MatchedPath`), and `http.response.status_code`. It also records `http.server.active_requests`. A request that matches no route has no `http.route`.
  - The reqwest client of the OTLP exporter needs the tokio reactor. So the providers use the tokio variants of the batch processors and the periodic reader. These are the opentelemetry_sdk features `experimental_trace_batch_span_processor_with_async_runtime`, `experimental_metrics_periodicreader_with_async_runtime`, and `experimental_logs_batch_log_processor_with_async_runtime`. They add no crate to the lock. The shutdown runs on a blocking thread.
  - `OTEL_METRIC_EXPORT_INTERVAL` (ms) sets the metric interval. The default is 30 s, as in Go.
- `todo-api`: the `worker` command, and the hourly periodic job `purge_done_todos` (`src/jobs.rs`). `serve` and `worker` start the telemetry with the service name `todo-api` (`<app>-api`). `cargo build --no-default-features` gives the R0.6 app, for the size and time comparison.

Tests (all offline, with `DATABASE_URL` for `#[sqlx::test]`):

- `kit/tests/jobs.rs`: unique by args (also key order in the args, a completed job, and an insert in a transaction), NOTIFY with a 600 s poll interval, the poll fallback without LISTEN, retry with backoff then discard, the rescuer, periodic jobs with three workers, a graceful stop, and the chaos test.
- The chaos test: 10 000 jobs, two workers, 8 slots each, a pool of 10 connections each, `rescue_after` 1 s, a leader TTL of 1 s, and a fixed backoff of 100 ms × attempt² (no jitter, so the test can check each retry time). The test kills a worker each time the completed count passes `k × 10 000 / 52`, for k from 1 to 50. A kill aborts the worker task. That drops the fetch loop, the running jobs, the listener, the leader loop, and the pool of that worker. A new worker with a new ID and a new pool replaces it. The two workers are killed in turn, so the leader dies too. The handler records its attempt, sleeps 0 to 3 ms, fails attempt 1 when `n` divides by 10, and then upserts its effect into `chaos_done` with a run counter.
- The rule for duplicates: delivery is at least once. A job runs again only when a kill stops its worker after the handler committed its effect and before the worker recorded the completion. The test handler is idempotent by design (an upsert that counts the runs). The test asserts that each job with more than one run also has a rescue entry in its errors. A handler that changes only the database can make its change and the completion in one transaction. The spike did not build that API (see the recommendation).
- `kit/tests/telemetry.rs`:
  - The metric names: a meter provider with the SDK `InMemoryMetricExporter`, the full kit router, and requests to a nested `/api` router (200, 500, and an unknown path with 404). The test checks the OTLP name, unit, and bounds. Then it converts the export to Prometheus series with the rules of the Prometheus and Alloy OTLP receivers (underscore escaping with suffixes, and `job` from `service.name`). It reads every file of the monitoring pack (`internal/templates/packs/monitoring/files/monitoring/`), and checks that each `http_server_request_duration_seconds*` series name and each label that the pack queries is in the app series.
  - The OTLP export (`kit/tests/otlp.rs`, its own test binary with a global subscriber): a fake collector on 127.0.0.1 receives the three signals. The test decodes the protobuf with `opentelemetry-proto` and `prost`. It checks the server span (name, kind, remote parent, attributes), the histogram, and that the handler log record has the trace ID of the `traceparent`.
- `todo-api/tests/jobs.rs`: the worker runs `purge_done_todos` against the app tables.
- The panel check †: `panel-check.sh` starts the local `grafana/otel-lgtm` image (`--pull never`). It runs `todo-api serve` with OTLP export to it, and sends 40 s of traffic (201, 200, 404, and 422 answers). Then it runs each query of the pack dashboard `http-red.json` through the Grafana Prometheus data source, with `$job` = `todo-api` and `$__rate_interval` = `1m`. It also searches Tempo and Loki through Grafana. Docker was available to this lane, so I ran it.

Machine: Apple M1 Max, 10 cores, 64 GB. Other heavy sessions ran at the same time. Each measurement has its 1-minute load average. Builds ran with `nice -n 10` and `CARGO_BUILD_JOBS=4`. Rust 1.99.1. PostgreSQL 18.6 on 127.0.0.1:55433.

## Numbers

Chaos test (debug build, `chaos_kill_the_worker_50_times`):

| Item | Run 1 | Run 2 |
|---|---|---|
| Load average (1 min), start to end | 11.6 to 13.7 (5 min avg 18.7) | 7.9 to 10.8 |
| Jobs | 10 000 | 10 000 |
| Kills | 50 (first at 193 completed, last at 9 618) | 50 (first at 196, last at 9 615) |
| Run time, insert done to all completed | 29.8 s | 24.2 s |
| Lost jobs (not `completed`, or no effect row) | 0 | 0 |
| Jobs in another state at the end | 0 | 0 |
| Jobs that completed twice | 0 (not possible: state check) | 0 |
| Jobs whose effect ran twice (idempotent handler) | 66 (67 extra runs) | 56 (56 extra runs) |
| Second runs without a kill | 0 | 0 |
| Rescued attempts | 256 | 262 |
| Planned failures on attempt 1 | 979 (the other 21 of 1 000: a kill came first) | 985 |
| Error entries with a wrong retry time (not 100 ms × attempt² ± 1 ms) | 0 of 1 235 | 0 of 1 247 |
| Next attempts that started before the retry time | 0 of 1 225 checked | 0 of 1 233 checked |
| Highest attempt | 3 | 3 |

Without kills (`chaos_baseline_without_kills`, ignored by default, run 2 conditions): 10 000 jobs in 12.7 s (about 790 jobs/s with 2 × 8 slots, debug build), 1 000 planned failures, 0 duplicate effects. In the full `cargo test --workspace` run, the chaos test ran next to the other job tests. The test binary took 42.5 s (load 17.6 to 22.5).

Other job tests:

| Test | Result |
|---|---|
| NOTIFY, poll interval 600 s, insert to `completed` | 118 ms for the first job, then 15 to 18 ms (5 jobs, the check polls every 10 ms) |
| Poll fallback without LISTEN (poll 100 ms, one job delayed 300 ms) | both done within 3 s |
| Retry then discard (`max_attempts` 4) | 4 error entries, each retry time equals the backoff ± 1 ms, no early attempt |
| Rescuer (a job left `running` 1 h ago by a dead worker) | completed on attempt 2, `attempted_by` = `dead`, `rescuer` |
| Periodic job, 250 ms, 3 workers for 2 s | 6 jobs (one leader). Three leaders would give about 21. |
| Unique by args | duplicate detected, also with other key order. A completed job does not block. |
| Graceful stop during a 300 ms job | the job completes before `run` returns |

Telemetry:

| Item | Result |
|---|---|
| OTLP metric | `http.server.request.duration`, unit `s`, f64 histogram, 14 bounds 0.005 to 10 s |
| Prometheus series after conversion | `http_server_request_duration_seconds_bucket`, `_count`, `_sum` |
| Labels | `http_request_method`, `http_route`, `http_response_status_code`, `job="todo-api"`, `le` |
| Names and labels that the pack queries (dashboards, rules, Alloy) | `http_server_request_duration_seconds`, `_bucket`, `_count`. Labels `http_request_method`, `http_response_status_code`, `http_route`, `job`, `le`. All present. |
| Route label | the pattern (`/api/todos/{id}`), never the path. No `http_route` for an unknown path. |
| OTLP HTTP to the fake collector | `traces` 1 187 B, `metrics` 838 B, `logs` 603 B, all `application/x-protobuf` |
| Server span | `GET /api/todos/{id}`, kind server, trace ID and parent from `traceparent` |
| Log record of the handler | has the trace ID of the request |

Panel check † (`panel-check.sh`, `grafana/otel-lgtm` 0.36.0, digest `sha256:57c04cbd…f9944ba2`, the pin in `stackversions.yaml`, load 14.2 at the start, 67 s):

| Panel of `http-red.json` | Series | First value |
|---|---|---|
| Requests per second | 1 | 19.0 req/s |
| Error ratio (5xx) | 0 | no 5xx traffic, so empty is correct |
| Duration p50, p95, p99 | 1, 1, 1 | 3.4 ms, 9.4 ms, 18.9 ms |
| Requests per second by status code | 4 | 201, 200, 404, 422 |
| Requests per second by route | 4 | |
| p95 duration by route | 4 | |
| 5xx per second by route | 0 | no 5xx traffic |
| `job` label values | | `otelcol-contrib`, `todo-api` |
| Tempo search `service.name=todo-api` | 5 traces | roots `GET /api/todos`, `GET /api/todos/{id}`, `POST /api/todos` |
| Loki `{service_name="todo-api"}` | 5 lines (first page) | |

Build cost of jobs and OTel (`todo-api`, clean target directory each time, `--no-default-features` is the R0.6 app):

| Measurement | R0.6 app | With jobs and OTel | Added | Load (1 min) |
|---|---|---|---|---|
| Packages (normal and build dependencies) | 199 | 226 | 27 | |
| Clean debug build | 43.3 s (user 104.3 s) | 45.6 s (user 124.8 s) | 2.3 s (user 20.5 s) | 12.7 to 18.9 |
| Clean release build | 80.0 s (user 243.9 s) | 86.8 s (user 285.2 s) | 6.8 s (user 41.3 s) | 12.4 to 18.9 |
| Debug binary | 34.6 MB | 49.0 MB | 14.4 MB | |
| Release binary (no LTO, not stripped) | 11.3 MB | 15.3 MB | 4.0 MB | |
| Release binary, stripped | 8.8 MB | 11.7 MB | 2.9 MB | |
| Debug target directory | 1.1 GB | 1.4 GB | 0.3 GB | |
| Release target directory | 0.72 GB | 0.82 GB | 0.10 GB | |
| Warm rebuild after an edit of `todos.rs` (runs 2 to 5 of 5) | 1.13 to 1.19 s | 1.24 to 1.33 s | about 0.1 s | 10.4 to 11.1 |

The first warm rebuild took 2.05 s and 2.26 s. Code size: `kit/src/jobs.rs` 744 lines and `kit/src/telemetry.rs` 403 lines, with doc comments and unit tests. The kit integration tests have 1 103 lines.

## Verdict

Verdict: go

The own queue passes the chaos test twice. 50 kills in 10 000 jobs lose no job, and no job completes twice. Every retry time follows the backoff, and no attempt starts before its retry time. Duplicate effects (56 and 66) come only from kills, as at-least-once delivery allows. The rescuer, the leader, LISTEN/NOTIFY with the poll fallback, periodic jobs, and unique jobs work with sqlx 0.9 and no extra crate. The OTel stack gives the exact series names and labels that the monitoring pack queries. The HTTP RED dashboard shows data against the pinned LGTM image, and Tempo and Loki have the traces and logs. Jobs and OTel add 27 packages, 2.3 s to a clean debug build, and 4.0 MB to the release binary, at a load average of 12 to 19.

## Recommendation

- Use the own queue for kit feature `jobs`. Keep the River schema and the SQL of this spike. Do not use `graphile_worker` or a River port. R2.8 copies `kit/src/jobs.rs` and the chaos test. R2.8 runs the chaos test again with a worker child process and `SIGKILL`.
- Write the at-least-once rule in the kit docs and in the `spinup g job` template: a handler must be idempotent. R2.8 adds `complete_tx`, so a handler that changes only the database can commit its change and the completion in one transaction (as River's `JobCompleteTx`). Then such a job has an exactly-once effect.
- The kit queries of the queue use `sqlx::query` at run time, not the `query!` macros. A published kit crate cannot use the `.sqlx/` directory of the app. The chaos and job tests cover these queries against PostgreSQL instead.
- Set the production defaults to `rescue_after` 1 h (River's default), a leader TTL of 10 s, a poll interval of 1 s, and a maintenance interval of 1 s. Expose these values and the backoff in `Config`.
- R2.8 also adds: job metrics (River-style `kit.job.duration` with `job.kind` and the result), a job span that links to the trace of the insert (store `traceparent` in `metadata`), the cleaner for finalized jobs, queue pause, cancel, and snooze, and a fetch cooldown. The baseline of about 790 jobs/s (debug build, two workers, 8 slots each) was enough for the chaos test. The spike did not tune the fetch loop.
- Use `opentelemetry` 0.33, `opentelemetry_sdk` 0.33 (feature `rt-tokio` and the three `*_with_async_runtime` features), `opentelemetry-otlp` 0.33 (`http-proto`, `reqwest-client`, no default features), `opentelemetry-appender-tracing` 0.33, and `tracing-opentelemetry` 0.34. Pin them together. The `*_with_async_runtime` features are "experimental" in 0.33. The fallback is the thread-based processors with `reqwest-blocking-client`. The spike did not measure that fallback, because it needs a crate feature that is not in the seed.
- Give each kit test that checks spans its own test binary with a global subscriber. Do not use a scoped subscriber (`tracing::subscriber::set_default`) next to other tests. Cause: when only one dispatcher is registered, tracing-core 0.1.36 computes the cached interest of a new callsite from the default dispatcher of the thread that hits the callsite first. A parallel test thread without a subscriber then caches "never" for the span callsite of `http_layer`, and the scoped test loses its spans. The first version of the OTLP test had this problem: it failed in 52 of 60 direct runs of its test binary. After the move to `kit/tests/otlp.rs`, it failed in 0 of 60. The app is not affected, because `kit::telemetry::init` installs a global subscriber.
- Put `http_layer` in `kit::app::router` (`router_with_metrics` for tests). Keep the metric names of this spike. Do not add the Go `http.server.request.count` counter: the pack does not query it.
- `opentelemetry-semantic-conventions` is not necessary. The spike writes the five attribute names as strings. `garde` and `tower-http` were in the seed, but the spike did not need them.
- Generated apps keep the service name `<app>-api`. The pack queries `job=<app>-api`.
- The Go kit also exports the Go runtime metrics. The Rust kit has no equivalent yet. The "Go runtime" dashboard of the pack needs a Rust replacement (tokio runtime metrics or process metrics) in R3.
- Not done: the panel check with a 5xx route (the two 5xx panels were empty because the traffic had no 5xx answer), and the jobs dashboard. The main loop can run `panel-check.sh` again: `DATABASE_URL=postgres://postgres:postgres@127.0.0.1:55433/spinup_r08 ./panel-check.sh` in `spikes-rs/r0-08-ref-jobs-otel/` (Docker and the local LGTM image are necessary). The database `spinup_r08` on 127.0.0.1:55433 holds the app tables for `cargo sqlx prepare --check --workspace`, which passes.
