# spinup performance result

- Commit: 3d475d30c8cd7f4c845cc2f1b49a55fe1dd74695
- Started: 2026-11-13T22:48:47-06:00; finished: 2026-11-13T22:59:32-06:00
- Machine: Apple M1 Max, 10 cores, 64 GiB, macOS 26.4.1 (arm64)
- Go: go version go1.27.3 darwin/arm64; GOFLAGS: `-p=4`; os.nice(10) for the whole perf process
- Repository: 3d475d30c8cd (orch/20261113-130519/integration)
- Scenario: 20 resources; cli suite `new --api-only --modules ci,jobs,mail`; app suite `new --modules ci,auth,jobs,mail`
- Load gate: wait while load1 >= 4.5 (at most 900 s for one series, 3600 s for the run)

## CLI suite

Load before: 1.91, after: 3.56; duration 27.9 s

| metric | unit | better | median | p90 | n |
|---|---|---|---|---|---|
| cli_binary_size | MB | lower | 21.1 |  | 1 |
| help_time | ms | lower | 11.5 | 11.7 | 30 |
| version_time | ms | lower | 11.5 | 12.1 | 30 |
| new_time | ms | lower | 42.6 | 49.4 | 10 |
| gen_one_resource_nodb_time | ms | lower | 111 | 116 | 10 |
| gen_resources_nodb_time | s | lower | 3.71 | 3.71 | 3 |
| upgrade_noop_time | ms | lower | 37.9 | 41.0 | 10 |
| db_migrate_time | ms | lower | 326 | 802 | 5 |
| peak_rss_new | MiB | lower | 25.2 | 25.5 | 5 |

<details><summary>Notes</summary>

- **cli_binary_size**: the stripped CLI (go build -trimpath -ldflags '-s -w' ./cmd/spinup)
- **help_time**: spinup --help, process wall time; n=30 after 1 warm-up discarded; load 1.91 -> 1.91
- **version_time**: spinup version, process wall time; n=30 after 1 warm-up discarded; load 1.91 -> 1.91
- **new_time**: spinup new --api-only --modules ci,jobs,mail --no-git in an empty dir; n=10 after 1 warm-up discarded; load 1.91 -> 1.91
- **gen_one_resource_nodb_time**: spinup g resource todo title:string done:bool due:date? priority:enum:low\|normal\|high --api-only in a new API-only app (the app is created before the timer); generation only: the .env files are hidden and DATABASE_URL is unset, so `g resource` skips its database post-steps; n=10 after 1 warm-up discarded; load 1.91 -> 1.91
- **gen_resources_nodb_time**: spinup g resource --api-only for the 20 resources of scripts/perf/resources.txt, summed process time (the app is created before the timer); generation only: the .env files are hidden and DATABASE_URL is unset, so `g resource` skips its database post-steps; n=3 after 1 warm-up discarded; load 1.91 -> 2.82
- **upgrade_noop_time**: spinup upgrade --offline on a new git app with no template change; n=10 after 1 warm-up discarded; load 2.82 -> 2.82
- **db_migrate_time**: spinup db migrate on the 20-resource API-only app, a new database for each sample. It runs `go run ./cmd/api migrate up`, so the link is included (the build cache is warm after the warm-up); n=5 after 1 warm-up discarded; load 2.82 -> 3.56
- **peak_rss_new**: maximum resident set size of spinup new --api-only, /usr/bin/time (children included); n=5 after 1 warm-up discarded; load 3.56 -> 3.56

</details>

## Generated app suite

Load before: 3.56, after: 4.37; duration 290.7 s

| metric | unit | better | median | p90 | n |
|---|---|---|---|---|---|
| app_cold_build | s | lower | 11.3 | 11.5 | 2 |
| app_incremental_build | s | lower | 1.23 | 1.25 | 5 |
| app_release_build_time | s | lower | 12.0 |  | 1 |
| app_release_binary_size | MB | lower | 28.3 |  | 1 |
| app_start_to_healthz | ms | lower | 11.9 | 14.1 | 20 |
| app_rps_healthz | req/s | higher | 77,188 | 78,092 | 3 |
| app_latency_p50_healthz (info) | ms | lower | 0.32 | 0.33 | 3 |
| app_latency_p90_healthz (info) | ms | lower | 0.79 | 0.79 | 3 |
| app_latency_p99_healthz (info) | ms | lower | 1.73 | 1.74 | 3 |
| app_latency_p999_healthz (info) | ms | lower | 2.85 | 3.00 | 3 |
| app_rps_list | req/s | higher | 5,395 | 5,431 | 3 |
| app_latency_p50_list (info) | ms | lower | 5.73 | 6.01 | 3 |
| app_latency_p90_list (info) | ms | lower | 7.28 | 8.08 | 3 |
| app_latency_p99_list (info) | ms | lower | 10.3 | 13.1 | 3 |
| app_latency_p999_list (info) | ms | lower | 24.2 | 24.8 | 3 |
| app_idle_rss | MiB | lower | 22.3 | 22.4 | 3 |
| app_rss_after_load | MiB | lower | 45.5 | 47.2 | 3 |
| app_login_latency | ms | lower | 43.5 | 45.3 | 20 |

<details><summary>Notes</summary>

- **app_cold_build**: go build -o <bin> ./cmd/api with a new empty GOCACHE (the standard library compiles too); the modules are already downloaded. No warm-up, because a warm-up is another cold build; n=2 after 0 warm-up discarded; load 3.91 -> 7.67
- **app_incremental_build**: go build after a one-string edit in api/internal/routes/perf_mark.go: the routes package compiles again, and the binary links again; n=5 after 1 warm-up discarded; load 4.36 -> 4.74
- **app_release_build_time**: cold release build as the Dockerfile builds it: CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" with a new GOCACHE. One sample: the binary is also the one of the runtime metrics; n=1 after 0 warm-up discarded; load 4.39 -> 5.63
- **app_release_binary_size**: the binary of app_release_build_time (stripped, the host target)
- **app_start_to_healthz**: release binary, SPINUP_ENV=production, a migrated database; spawn to the first 200 from /healthz, polled every 2 ms with a new connection; n=20 after 1 warm-up discarded; load 4.47 -> 4.47
- **app_rps_healthz**: GET /healthz. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, the client runs on all cores of the same machine) against the release binary; one new server for each sample; n=3 after 1 warm-up discarded; load 4.47 -> 14.07
- **app_latency_p50_healthz**: GET /healthz, 32 concurrent. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, the client runs on all cores of the same machine) against the release binary; one new server for each sample
- **app_latency_p50_healthz** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **app_latency_p90_healthz**: GET /healthz, 32 concurrent. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, the client runs on all cores of the same machine) against the release binary; one new server for each sample
- **app_latency_p90_healthz** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **app_latency_p99_healthz**: GET /healthz, 32 concurrent. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, the client runs on all cores of the same machine) against the release binary; one new server for each sample
- **app_latency_p99_healthz** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **app_latency_p999_healthz**: GET /healthz, 32 concurrent. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, the client runs on all cores of the same machine) against the release binary; one new server for each sample
- **app_latency_p999_healthz** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **app_rps_list**: GET /api/notes?limit=50 with the session cookie of the perf user: one session lookup and a list of 50 rows as JSON (100 seeded rows of that user). scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, the client runs on all cores of the same machine) against the release binary; one new server for each sample
- **app_latency_p50_list**: GET /api/notes?limit=50 with the session cookie of the perf user: one session lookup and a list of 50 rows as JSON (100 seeded rows of that user). scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, the client runs on all cores of the same machine) against the release binary; one new server for each sample
- **app_latency_p50_list** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **app_latency_p90_list**: GET /api/notes?limit=50 with the session cookie of the perf user: one session lookup and a list of 50 rows as JSON (100 seeded rows of that user). scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, the client runs on all cores of the same machine) against the release binary; one new server for each sample
- **app_latency_p90_list** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **app_latency_p99_list**: GET /api/notes?limit=50 with the session cookie of the perf user: one session lookup and a list of 50 rows as JSON (100 seeded rows of that user). scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, the client runs on all cores of the same machine) against the release binary; one new server for each sample
- **app_latency_p99_list** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **app_latency_p999_list**: GET /api/notes?limit=50 with the session cookie of the perf user: one session lookup and a list of 50 rows as JSON (100 seeded rows of that user). scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, the client runs on all cores of the same machine) against the release binary; one new server for each sample
- **app_latency_p999_list** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **app_idle_rss**: ps RSS 2 s after the first 200 from /healthz, before any load
- **app_rss_after_load**: ps RSS right after the two load runs (the Go runtime may keep freed pages). The server never ran a login, so the argon2id buffer is not in this number
- **app_login_latency**: POST /api/auth/login for the perf user, sequential, one keep-alive connection, the release binary (one argon2id check each; the loopback client is exempt from the rate limit); n=20 after 1 warm-up discarded; load 4.37 -> 4.37

</details>

## Go benchmarks suite

Load before: 4.37, after: 2.79; duration 177.6 s

| metric | unit | better | median | p90 | n |
|---|---|---|---|---|---|
| internal/manifest BenchmarkLockReadValidateWrite ns/op | ns/op | lower | 5,793,856 | 5,904,849 | 6 |
| internal/manifest BenchmarkLockReadValidateWrite B/op (deterministic) | B/op | lower | 302,443 | 303,765 | 6 |
| internal/manifest BenchmarkLockReadValidateWrite allocs/op (deterministic) | allocs/op | lower | 2,448 | 2,449 | 6 |
| internal/render BenchmarkRenderAPI ns/op | ns/op | lower | 652,916 | 663,140 | 6 |
| internal/render BenchmarkRenderAPI B/op (deterministic) | B/op | lower | 1,144,086 | 1,144,504 | 6 |
| internal/render BenchmarkRenderAPI allocs/op (deterministic) | allocs/op | lower | 5,036 | 5,036 | 6 |
| internal/vfs BenchmarkDiff40 ns/op | ns/op | lower | 1,584,596 | 1,600,384 | 6 |
| internal/vfs BenchmarkDiff40 B/op (deterministic) | B/op | lower | 146,387 | 146,391 | 6 |
| internal/vfs BenchmarkDiff40 allocs/op (deterministic) | allocs/op | lower | 1,546 | 1,546 | 6 |
| internal/vfs BenchmarkApply40 ns/op | ns/op | lower | 27,754,980 | 28,226,803 | 6 |
| internal/vfs BenchmarkApply40 B/op (deterministic) | B/op | lower | 1,571,058 | 1,571,078 | 6 |
| internal/vfs BenchmarkApply40 allocs/op (deterministic) | allocs/op | lower | 2,452 | 2,453 | 6 |
| kit/app BenchmarkMiddlewareChain/generated ns/op | ns/op | lower | 2,844 | 3,254 | 6 |
| kit/app BenchmarkMiddlewareChain/generated B/op (deterministic) | B/op | lower | 2,234 | 2,234 | 6 |
| kit/app BenchmarkMiddlewareChain/generated allocs/op (deterministic) | allocs/op | lower | 29.0 | 29.0 | 6 |
| kit/app BenchmarkMiddlewareChain/provided ns/op | ns/op | lower | 2,533 | 2,538 | 6 |
| kit/app BenchmarkMiddlewareChain/provided B/op (deterministic) | B/op | lower | 2,202 | 2,202 | 6 |
| kit/app BenchmarkMiddlewareChain/provided allocs/op (deterministic) | allocs/op | lower | 28.0 | 28.0 | 6 |
| kit/app BenchmarkSPAAsset ns/op | ns/op | lower | 32,545 | 32,738 | 6 |
| kit/app BenchmarkSPAAsset B/op (deterministic) | B/op | lower | 135,378 | 135,381 | 6 |
| kit/app BenchmarkSPAAsset allocs/op (deterministic) | allocs/op | lower | 52.0 | 52.0 | 6 |
| kit/app BenchmarkSPAIndex ns/op | ns/op | lower | 5,269 | 5,280 | 6 |
| kit/app BenchmarkSPAIndex B/op (deterministic) | B/op | lower | 4,358 | 4,359 | 6 |
| kit/app BenchmarkSPAIndex allocs/op (deterministic) | allocs/op | lower | 52.0 | 52.0 | 6 |
| kit/auth BenchmarkTake ns/op | ns/op | lower | 22.5 | 22.7 | 6 |
| kit/auth BenchmarkTake B/op (deterministic) | B/op | lower | 1.00 | 1.00 | 6 |
| kit/auth BenchmarkTake allocs/op (deterministic) | allocs/op | lower | 0.00 | 0.00 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/DecodeJSON ns/op | ns/op | lower | 834 | 838 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/DecodeJSON MB/s | MB/s | higher | 330 | 331 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/DecodeJSON B/op (deterministic) | B/op | lower | 112 | 112 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/DecodeJSON allocs/op (deterministic) | allocs/op | lower | 3.00 | 3.00 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/UnmarshalRead ns/op | ns/op | lower | 781 | 783 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/UnmarshalRead MB/s | MB/s | higher | 352 | 355 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/UnmarshalRead B/op (deterministic) | B/op | lower | 112 | 112 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/UnmarshalRead allocs/op (deterministic) | allocs/op | lower | 3.00 | 3.00 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/UnmarshalDecode ns/op | ns/op | lower | 1,418 | 1,480 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/UnmarshalDecode MB/s | MB/s | higher | 194 | 196 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/UnmarshalDecode B/op (deterministic) | B/op | lower | 6,080 | 6,080 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/UnmarshalDecode allocs/op (deterministic) | allocs/op | lower | 14.0 | 14.0 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/V1 ns/op | ns/op | lower | 1,510 | 1,514 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/V1 MB/s | MB/s | higher | 182 | 184 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/V1 B/op (deterministic) | B/op | lower | 1,578 | 1,578 | 6 |
| kit/httpx BenchmarkDecodeJSON/Resource/V1 allocs/op (deterministic) | allocs/op | lower | 12.0 | 12.0 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/DecodeJSON ns/op | ns/op | lower | 49,404 | 49,673 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/DecodeJSON MB/s | MB/s | higher | 1,327 | 1,332 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/DecodeJSON B/op (deterministic) | B/op | lower | 65,736 | 65,764 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/DecodeJSON allocs/op (deterministic) | allocs/op | lower | 4.00 | 4.00 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/UnmarshalRead ns/op | ns/op | lower | 49,289 | 49,951 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/UnmarshalRead MB/s | MB/s | higher | 1,330 | 1,332 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/UnmarshalRead B/op (deterministic) | B/op | lower | 65,748 | 65,764 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/UnmarshalRead allocs/op (deterministic) | allocs/op | lower | 4.00 | 4.00 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/UnmarshalDecode ns/op | ns/op | lower | 62,702 | 65,743 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/UnmarshalDecode MB/s | MB/s | higher | 1,045 | 1,053 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/UnmarshalDecode B/op (deterministic) | B/op | lower | 201,952 | 201,953 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/UnmarshalDecode allocs/op (deterministic) | allocs/op | lower | 22.0 | 22.0 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/V1 ns/op | ns/op | lower | 143,253 | 145,672 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/V1 MB/s | MB/s | higher | 457 | 464 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/V1 B/op (deterministic) | B/op | lower | 197,343 | 197,362 | 6 |
| kit/httpx BenchmarkDecodeJSON/64KiB/V1 allocs/op (deterministic) | allocs/op | lower | 20.0 | 20.0 | 6 |
| kit/httpx BenchmarkCheckJSONBody ns/op | ns/op | lower | 989 | 1,007 | 6 |
| kit/httpx BenchmarkCheckJSONBody MB/s | MB/s | higher | 278 | 279 | 6 |
| kit/httpx BenchmarkCheckJSONBody B/op (deterministic) | B/op | lower | 1,000 | 1,000 | 6 |
| kit/httpx BenchmarkCheckJSONBody allocs/op (deterministic) | allocs/op | lower | 9.00 | 9.00 | 6 |
| kit/httpx BenchmarkWriteJSON ns/op | ns/op | lower | 692 | 697 | 6 |
| kit/httpx BenchmarkWriteJSON B/op (deterministic) | B/op | lower | 1,400 | 1,400 | 6 |
| kit/httpx BenchmarkWriteJSON allocs/op (deterministic) | allocs/op | lower | 15.0 | 15.0 | 6 |
| kit/logx BenchmarkAccessLine/Plain ns/op | ns/op | lower | 1,330 | 1,354 | 6 |
| kit/logx BenchmarkAccessLine/Plain B/op (deterministic) | B/op | lower | 144 | 144 | 6 |
| kit/logx BenchmarkAccessLine/Plain allocs/op (deterministic) | allocs/op | lower | 4.00 | 4.00 | 6 |
| kit/logx BenchmarkAccessLine/URL ns/op | ns/op | lower | 3,011 | 3,039 | 6 |
| kit/logx BenchmarkAccessLine/URL B/op (deterministic) | B/op | lower | 322 | 323 | 6 |
| kit/logx BenchmarkAccessLine/URL allocs/op (deterministic) | allocs/op | lower | 10.0 | 10.0 | 6 |
| kit/x/flags BenchmarkResolve ns/op | ns/op | lower | 53.4 | 54.0 | 6 |
| kit/x/flags BenchmarkResolve B/op (deterministic) | B/op | lower | 0.00 | 0.00 | 6 |
| kit/x/flags BenchmarkResolve allocs/op (deterministic) | allocs/op | lower | 0.00 | 0.00 | 6 |
| kit/x/flags BenchmarkEnabled/no_override ns/op | ns/op | lower | 62.6 | 63.4 | 6 |
| kit/x/flags BenchmarkEnabled/no_override B/op (deterministic) | B/op | lower | 0.00 | 0.00 | 6 |
| kit/x/flags BenchmarkEnabled/no_override allocs/op (deterministic) | allocs/op | lower | 0.00 | 0.00 | 6 |
| kit/x/flags BenchmarkEnabled/override_table_active ns/op | ns/op | lower | 71.8 | 72.0 | 6 |
| kit/x/flags BenchmarkEnabled/override_table_active B/op (deterministic) | B/op | lower | 0.00 | 0.00 | 6 |
| kit/x/flags BenchmarkEnabled/override_table_active allocs/op (deterministic) | allocs/op | lower | 0.00 | 0.00 | 6 |

## web suite

Load before: 2.79, after: 6.46; duration 147.8 s

| metric | unit | better | median | p90 | n |
|---|---|---|---|---|---|
| web_bundle_raw_bytes (deterministic) | B | lower | 578,548 |  | 1 |
| web_bundle_br_bytes (deterministic) | B | lower | 158,359 |  | 1 |
| web_bundle_gz_bytes (deterministic) | B | lower | 183,625 |  | 1 |
| web_first_load_br_bytes_login (deterministic) | B | lower | 101,034 |  | 1 |
| web_build_time | s | lower | 4.51 | 4.53 | 3 |
| web_start_to_healthz | ms | lower | 223 | 245 | 10 |
| web_ssr_rps_page | req/s | higher | 824 | 839 | 3 |
| web_ssr_rps_login | req/s | higher | 4,987 | 5,180 | 3 |
| web_latency_p50_page (info) | ms | lower | 38.4 | 42.1 | 3 |
| web_latency_p50_login (info) | ms | lower | 5.90 | 6.38 | 3 |
| web_latency_p90_page (info) | ms | lower | 48.6 | 53.6 | 3 |
| web_latency_p90_login (info) | ms | lower | 8.43 | 10.4 | 3 |
| web_latency_p99_page (info) | ms | lower | 60.9 | 67.2 | 3 |
| web_latency_p99_login (info) | ms | lower | 12.3 | 12.7 | 3 |
| web_latency_p999_page (info) | ms | lower | 76.3 | 84.0 | 3 |
| web_latency_p999_login (info) | ms | lower | 18.4 | 21.0 | 3 |
| web_ttfb_page (info) | ms | lower | 4.07 | 4.28 | 3 |
| web_idle_rss | MiB | lower | 79.0 | 79.2 | 3 |
| web_rss_after_load | MiB | lower | 516 | 519 | 3 |
| web_api_requests_per_page (deterministic) | req | lower | 2.00 |  | 1 |
| embed_binary_size (deterministic) | B | lower | 35,110,130 |  | 1 |
| embed_index_rps | req/s | higher | 53,618 | 53,909 | 3 |

<details><summary>Notes</summary>

- **web_bundle_raw_bytes**: scripts/perf/bundle.py measure: 37 .js and .css files under _app/immutable of the client build; the .br and .gz siblings are summed apart. Largest: _app/immutable/chunks/C8bpp1C7.js 78299, _app/immutable/chunks/JEReDw98.js 70826, _app/immutable/chunks/e3S_5ZKy.js 67927, _app/immutable/assets/0.C2KZFVXq.css 67804, _app/immutable/chunks/Dcw88NNN.js 53718
- **web_bundle_br_bytes**: scripts/perf/bundle.py measure: 37 .js and .css files under _app/immutable of the client build; the .br and .gz siblings are summed apart. Largest: _app/immutable/chunks/C8bpp1C7.js 78299, _app/immutable/chunks/JEReDw98.js 70826, _app/immutable/chunks/e3S_5ZKy.js 67927, _app/immutable/assets/0.C2KZFVXq.css 67804, _app/immutable/chunks/Dcw88NNN.js 53718
- **web_bundle_gz_bytes**: scripts/perf/bundle.py measure: 37 .js and .css files under _app/immutable of the client build; the .br and .gz siblings are summed apart. Largest: _app/immutable/chunks/C8bpp1C7.js 78299, _app/immutable/chunks/JEReDw98.js 70826, _app/immutable/chunks/e3S_5ZKy.js 67927, _app/immutable/assets/0.C2KZFVXq.css 67804, _app/immutable/chunks/Dcw88NNN.js 53718
- **web_first_load_br_bytes_login**: the brotli bytes of the 21 modulepreload and script targets of the anonymous GET /login (the plain size when a file has no .br sibling); CSS not counted
- **web_build_time**: pnpm build (vite build with adapter-node and precompress) of the generated app after the setup build, with the packages installed; n=3 after 0 warm-up discarded; load 2.59 -> 2.38
- **web_start_to_healthz**: node --import <instrumentation> build (the start command of Dockerfile.web), NODE_ENV=production; spawn to the first 200 from /healthz, polled every 5 ms with a new connection; n=10 after 1 warm-up discarded; load 2.38 -> 2.38
- **web_ssr_rps_page**: GET /notes with the session cookie: SSR of the list page (20 of 100 seeded notes), one /api/session and one /api/notes call to the Go API. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, no compression, the client runs on the same machine) against one Node process; one new Node server for each sample; the API runs on the same machine; n=3 after 1 warm-up discarded; load 2.38 -> 4.1
- **web_ssr_rps_login**: anonymous GET /login: SSR of the sign-in page, no API call. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, no compression, the client runs on the same machine) against one Node process; one new Node server for each sample; the API runs on the same machine
- **web_latency_p50_page**: GET /notes with the session cookie: SSR of the list page (20 of 100 seeded notes), one /api/session and one /api/notes call to the Go API. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, no compression, the client runs on the same machine) against one Node process; one new Node server for each sample; the API runs on the same machine
- **web_latency_p50_page** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **web_latency_p50_login**: anonymous GET /login: SSR of the sign-in page, no API call. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, no compression, the client runs on the same machine) against one Node process; one new Node server for each sample; the API runs on the same machine
- **web_latency_p50_login** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **web_latency_p90_page**: GET /notes with the session cookie: SSR of the list page (20 of 100 seeded notes), one /api/session and one /api/notes call to the Go API. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, no compression, the client runs on the same machine) against one Node process; one new Node server for each sample; the API runs on the same machine
- **web_latency_p90_page** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **web_latency_p90_login**: anonymous GET /login: SSR of the sign-in page, no API call. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, no compression, the client runs on the same machine) against one Node process; one new Node server for each sample; the API runs on the same machine
- **web_latency_p90_login** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **web_latency_p99_page**: GET /notes with the session cookie: SSR of the list page (20 of 100 seeded notes), one /api/session and one /api/notes call to the Go API. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, no compression, the client runs on the same machine) against one Node process; one new Node server for each sample; the API runs on the same machine
- **web_latency_p99_page** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **web_latency_p99_login**: anonymous GET /login: SSR of the sign-in page, no API call. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, no compression, the client runs on the same machine) against one Node process; one new Node server for each sample; the API runs on the same machine
- **web_latency_p99_login** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **web_latency_p999_page**: GET /notes with the session cookie: SSR of the list page (20 of 100 seeded notes), one /api/session and one /api/notes call to the Go API. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, no compression, the client runs on the same machine) against one Node process; one new Node server for each sample; the API runs on the same machine
- **web_latency_p999_page** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **web_latency_p999_login**: anonymous GET /login: SSR of the sign-in page, no API call. scripts/perf/loadgen -c 32 -d 5s -warmup 1s (keep-alive, no compression, the client runs on the same machine) against one Node process; one new Node server for each sample; the API runs on the same machine
- **web_latency_p999_login** is informational: the same load run as the req/s metric; latency percentiles are noisy on a shared machine
- **web_ttfb_page**: p50 time to first byte of GET /notes with loadgen -c 1 -d 5s after the RSS reading
- **web_ttfb_page** is informational: one connection, sequential requests; time to first byte is noisy on a shared machine
- **web_idle_rss**: ps RSS of the Node process 2 s after the first 200 from /healthz, before any load
- **web_rss_after_load**: ps RSS of the Node process right after the page and the sign-in load runs
- **web_api_requests_per_page**: API access-log records (200) for each signed-in GET /notes (100) of a page load run with loadgen -n 100 -c 4 and no warm-up
- **embed_binary_size**: api/bin/api of spinup build in an embed app (modules ci; go build -tags embedweb -trimpath, not stripped) with the precompressed web files inside
- **embed_index_rps**: GET / with Accept-Encoding: br, gzip against the embed binary (SPINUP_ENV=production, LOG_LEVEL=info): index.html.br from the embedded file index. loadgen -c 32 -d 5s -warmup 1s; one server for all samples; n=3 after 1 warm-up discarded; load 3.62 -> 6.46

</details>

## Run notes

- Load gate: 20 gates, 0 timed out (the series then ran under load).

