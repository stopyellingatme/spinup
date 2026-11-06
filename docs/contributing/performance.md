---
title: Performance loop
parent: Contributing
nav_order: 13
---

# Performance loop

The performance loop measures spinup and a generated app, and it finds regressions.
One command measures the CLI, the build of a generated app, and the generated API under load.
A second command compares two results, and it fails when a metric got worse.
Run the loop before and after a change that can make spinup or the kit slower.

<!-- doctest: skip -->
```console
$ python3 -I scripts/perf/perf.py run --suites cli,app,micro
...
perf: results in perf/results/20261105-220330.json and perf/results/20261105-220330.md
$ python3 -I scripts/perf/perf.py compare perf/results/base.json perf/results/20261105-220330.json
...
summary: 41 same, 2 better
```

The Makefile has the same two commands as `make perf` and `make perf-compare`.

## How it works

The loop is `scripts/perf/perf.py`. It uses only the Python 3 standard library.
It builds the CLI and the load generator once. Then it runs the suites in the order `cli`, `app`, `micro`.

- Each series of samples starts with a load gate. The gate waits while the 1-minute load average is at or above 45 percent of the cores.
- Each series discards its first sample (the warm-up). The record keeps the median and the p90 of the other samples.
- The whole run has nice 10, and every `go` command has `GOFLAGS=-p=4`. The machine stays usable for other work.
- The loop writes the JSON after each suite. If a suite fails, the loop keeps the records of that suite so far, runs the next suite, and exits 1.

The `app` suite and the `db_migrate_time` metric need a PostgreSQL server.
The loop uses `DATABASE_URL`, or the test server `postgres://postgres:postgres@127.0.0.1:55433/postgres?sslmode=disable` when `DATABASE_URL` is not set.
The loop creates its own databases on that server and drops them at the end. It needs `psql` on `PATH`.

## Run the loop

Run all suites for a baseline. A full run takes 15 to 40 minutes, because the `app` suite does three cold builds.

<!-- doctest: skip -->
```console
$ python3 -I scripts/perf/perf.py run
```

Run some suites in quick mode while you work. Quick mode takes fewer samples and shorter load runs.

<!-- doctest: skip -->
```console
$ python3 -I scripts/perf/perf.py run --suites cli,micro --quick --out /tmp/cli.json
```

If you do not give `--out`, the result goes to `perf/results/<timestamp>.json`.
The Markdown table goes next to the JSON, with the extension `.md`. Git ignores `perf/results/`.

## Read a result

Open the Markdown file. It has one table for each suite:

```text
| metric | unit | better | median | p90 | n |
|---|---|---|---|---|---|
| help_time | ms | lower | 13.5 | 13.8 | 5 |
| new_time | ms | lower | 238 | 241 | 3 |
```

- `median` and `p90` come from the samples after the warm-up. `n` is the number of these samples.
- `better` tells if a lower or a higher value is better.
- A metric with `(info)` is informational. It comes from the same measurement as another metric, or it is noisy by nature. The compare shows it, but it does not fail on it.
- The notes under each table tell how the loop measured each metric, and the load average before and after the series.
- The "Run notes" section tells how many load gates timed out. If many gates timed out, the machine was busy, and the numbers are less exact.

The JSON has the same data and more:

| Key | Contents |
|---|---|
| `meta` | the machine, the Go version, `GOFLAGS`, the git sha and branch, quick mode, the scenario, and the load gate settings |
| `suites` | for each suite: the `uptime` output and the load average before and after, the duration, and the error (when the suite failed) |
| `records` | one record for each metric: `suite`, `metric`, `unit`, `samples`, `n`, `median`, `p90`, `better`, `note`, `informational` |
| `gate_events` | each load gate: how long it waited, and if it timed out |

## Compare two results

Give the base result first, then the new result:

<!-- doctest: skip -->
```console
$ python3 -I scripts/perf/perf.py compare base.json new.json
...
metric                     unit  better  base  new  ratio  verdict
-------------------------  ----  ------  ----  ---  -----  -------
cli/new_time               ms    lower    238  357   1.50  WORSE
...
perf: 1 primary metrics got worse by more than 10%: cli/new_time
```

The ratio is the new median divided by the base median.
A metric is `WORSE` when it moved in the wrong direction by more than the threshold. The default threshold is 10 percent.
The compare exits 1 when a primary metric is `WORSE`. It exits 0 for `same`, `better`, `worse (info)`, `new`, and `missing`.

Change the threshold with `--threshold`. The value is a fraction:

<!-- doctest: skip -->
```console
$ python3 -I scripts/perf/perf.py compare base.json new.json --threshold 0.25
```

Compare only results from the same machine. The compare prints a warning when the CPUs are different, or when only one result is a quick run.
If a timing metric is `WORSE`, run both sides again on an idle machine before you look for a cause.
Sizes and `allocs/op` do not change with the load, so trust a change in them at once.

`--md <file>` also writes the compare table as Markdown.
`perf.py report <result.json>` writes the Markdown table of one result again.

## Add a Go benchmark

The `micro` suite finds every package that has a `func Benchmark...` in a `_test.go` file.
It looks in the root module, `kit/`, and `plugin-sdk/`. You do not register a benchmark.
The suite runs `go test -run '^$' -bench . -benchmem -count 6` for each module, and records `ns/op`, `B/op`, `allocs/op`, and each custom `b.ReportMetric` unit.
The metric name is `<package> <benchmark> <unit>`, such as `kit/auth BenchmarkTake ns/op`.

Use `b.Loop()` in a new benchmark. Keep the setup out of the loop, so the benchmark measures one operation.

## Reference

### `perf.py run` flags

| Flag | Default | Example |
|---|---|---|
| `--suites` | `cli,app,micro` | `--suites app` |
| `--quick` | off | `--quick` |
| `--out` | `perf/results/<timestamp>.json` | `--out /tmp/a.json` |
| `--database-url` | `DATABASE_URL`, or the test server on port 55433 | `--database-url postgres://u:p@127.0.0.1:5432/postgres` |
| `--load-limit` | 45 percent of the cores | `--load-limit 3` |
| `--gate-max-s` | 900 (quick: 30) | `--gate-max-s 60` |
| `--gate-budget-s` | 3600 (quick: 120) | `--gate-budget-s 0` |
| `--min-free-gb` | 6 for `app`, 2 for `cli` (half in quick mode) | `--min-free-gb 0` |

`--gate-budget-s` is the total wait of all load gates in one run. After the budget, the gates do not wait.

### The suites

The scenario app has the 20 resources of `scripts/perf/resources.txt`.

| Suite | Metric | What it measures |
|---|---|---|
| `cli` | `cli_binary_size` | the size of the stripped CLI |
| `cli` | `help_time`, `version_time` | the start-up of `spinup --help` and `spinup version` |
| `cli` | `new_time` | `spinup new --api-only --modules ci,jobs,mail --no-git` |
| `cli` | `gen_one_resource_nodb_time` | one `spinup g resource` in a new app, with no database |
| `cli` | `gen_resources_nodb_time` | the 20 `spinup g resource` calls, with no database |
| `cli` | `upgrade_noop_time` | `spinup upgrade --offline` with no template change |
| `cli` | `db_migrate_time` | `spinup db migrate` on the 20-resource app, on a new database |
| `cli` | `peak_rss_new` | the peak memory of `spinup new` |
| `app` | `app_cold_build`, `app_incremental_build` | `go build ./cmd/api` with an empty build cache, and after a one-line edit |
| `app` | `app_release_build_time`, `app_release_binary_size` | the release build as the Dockerfile does it, and the binary size |
| `app` | `app_start_to_healthz` | from the start of the release binary to the first 200 from `/healthz` |
| `app` | `app_idle_rss`, `app_rss_after_load` | the memory of the API before and after the load runs |
| `app` | `app_rps_healthz`, `app_rps_list` | requests per second on `/healthz`, and on `GET /api/notes?limit=50` with a session |
| `app` | `app_latency_<p>_healthz`, `app_latency_<p>_list` | the p50, p90, p99, and p99.9 latency of the same load runs (informational) |
| `app` | `app_login_latency` | one `POST /api/auth/login` (one argon2id check) |
| `micro` | `<package> <benchmark> <unit>` | the Go benchmarks of the repository |

The `app` suite generates the app with the modules `ci,auth,jobs,mail`, because the login and the session need the `auth` module.

### The load generator

`scripts/perf/loadgen` replaces Apache Bench. It is a Go program with no dependencies outside the standard library.
Its workers send keep-alive requests on all cores, so the client does not limit a fast server.
It prints one JSON object with `rps`, `latency_ms` (`p50`, `p90`, `p99`, `p999`, `max`, `mean`), `errors`, and the status counts.
It exits 1 when a request failed.

<!-- doctest: skip -->
```console
$ go run ./scripts/perf/loadgen -url http://127.0.0.1:8080/healthz -c 32 -d 5s -warmup 1s
{
  "url": "http://127.0.0.1:8080/healthz",
  "concurrency": 32,
...
```

| Flag | Default | Example |
|---|---|---|
| `-url` | none (required) | `-url http://127.0.0.1:8080/healthz` |
| `-c` | 32 | `-c 64` |
| `-n` | none: give `-n` or `-d` | `-n 20000` |
| `-d` | none: give `-n` or `-d` | `-d 5s` |
| `-warmup` | 0 | `-warmup 1s` |
| `-cookie` | none | `-cookie 'session=abc'` |
| `-expect` | 200 | `-expect 204` |
| `-timeout` | 10s | `-timeout 2s` |

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| The run waits at `load gate` for minutes | The machine is busy | Stop other work, or set a lower `--gate-budget-s`. The notes then say `load_gate_timeout`. |
| `cannot create the database` | The PostgreSQL server is not running, or `DATABASE_URL` is wrong | Start the test server, or set `DATABASE_URL` to a server you own. |
| `DATABASE_URL uses port 55432` | That port belongs to another project | Use the spinup test server on port 55433. |
| A timing metric is `WORSE`, and nothing changed | Noise from the load on the machine | Run both sides again on an idle machine. Compare the load averages in the two results. |
| `loadgen saw failed requests` | The API answered an error under load | Read the app log in the error. Check that the route and the session still work. |

## See also

- [Docker-in-Docker deploy suite](dind.md)
- [Browser end-to-end suite](e2e.md)
- [Writing the docs](writing-docs.md)
