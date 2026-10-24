# R0.7 Reference app B: auth

Issue: #15. Spike code: `spikes-rs/r0-07-ref-auth/`.

## Question

Can the Rust kit carry the full auth stack of the Go `kit/auth` with the same contract? The stack is sessions with a hashed token, argon2id with a bounded hash queue, the login lockout, the cross-origin check, the per-client rate limit of ADR 0016, password reset with mail, and OIDC. Which OIDC crate do we use, and how do we handle `RUSTSEC-2023-0071`?

## Method

I copied the R0.6 app (`kit`, `todo-api`, `.cargo/config.toml`) and added the auth stack to the kit. I did not change the R0.6 directory.

- `kit/src/auth/store.rs`: an own sqlx `SessionStore` for tower-sessions 0.15. The table key is the hex SHA-256 of the cookie value. The table has a `user_id` column, which the store fills from `auth.user_id`.
- `kit/src/auth/mod.rs`: `Auth`, `Options`, the middleware, and the `CurrentUser` extractor. The middleware order is the Go order: default CSP, rate limit, cross-origin check, session, user load. The rate limit runs before any session or database work.
- `kit/src/auth/password.rs`: argon2id PHC strings with the Go parameters (64 MiB, t=3, p=4, 16-byte salt, 32-byte key). The hash runs on `spawn_blocking` behind two tokio semaphores. A full queue or a wait over 2 s answers 503 with `Retry-After: 1`. The middleware then gives the rate limit token back.
- `kit/src/auth/ratelimit.rs` and `clientip.rs`: a port of `ratelimit.go` and `clientip.go` (GCRA, the class table, `/64` and `/48` buckets, the infrastructure bucket, eviction, sweep, and the environment parser). `goduration.rs` formats durations as Go does, so the error texts are equal.
- `kit/src/auth/csrf.rs`: the check of Go's `http.CrossOriginProtection`.
- `kit/src/auth/handlers.rs`, `queries.rs`: register, login (lockout with an advisory lock), logout, and session. The texts are the Go texts.
- `kit/src/auth/reset.rs`, `kit/src/mail.rs`: password reset with lettre 0.11 (SMTP with rustls, and the file transport).
- `kit/src/auth/oidc.rs`: OIDC with `oauth2` 5 (authorization URL, PKCE, code exchange) and `jsonwebtoken` 11 (ID token check). A small adapter sends the oauth2 requests through reqwest 0.13.
- `oidc-compare/`: the same steps with `openidconnect` 4, for the comparison only. The app does not depend on it.
- `kit/src/auth/fake_issuer.rs` (feature `test-util`): a local OIDC provider with discovery, JWKS, authorization, and token endpoints. It signs RS256 tokens with an RSA key that it makes at start. It holds the shared case tables of `oidc_test.go`.
- `todo-api`: the auth migrations `00003` to `00005` (the Go auth pack migrations, with a `jsonb` session table and `user_id`), the wiring in `main.rs`, and `tests/auth_ratelimit.rs`.

Tests (91, all pass): kit unit tests 48, `kit/tests/auth.rs` 15, `oidc.rs` 6, `ratelimit.rs` 5, `reset.rs` 5, `oidc-compare` 2, `todo-api` 10. The database tests use `#[sqlx::test]`. The Go tables are copied with their values unchanged: `TestParseProxySet`, `TestResolveClient`, `TestForwardedEntriesReadsFromTheRight`, `TestRouteClass`, every `TestLimiter*`, `TestParseLimitSpec`, `TestResolveRates`, `TestRateLimitMiddleware`, `TestRateLimitWithoutLimiter`, `TestRateLimitTrustedProxies`, `TestNewRateLimitErrors`, `TestRateLimitHandlers`, `TestNormalizeEmail`, `TestOIDCRedirect`, the token cases of `TestOIDCFlow`, and `TestOIDCEmailVerifiedClaim`.

`todo-api/tests/auth_ratelimit.rs` replays `testdata/script/auth_ratelimit.txtar`. It starts the `todo-api` binary with the environment of the script and sends the same requests with reqwest.

Other commands: `cargo sqlx prepare --workspace` against `spinup_r07` on 55433 (32 queries in `.sqlx/`), `cargo deny --offline check advisories bans licenses` (cargo-deny 0.20.2, advisory database of 2026-10-24), and `kit/examples/hashbench.rs`. For the Go hash time I ran `argon2.IDKey` from golang.org/x/crypto v0.58.0 in a scratch program outside the repository.

Machine: Apple M1 Max, 10 cores, 64 GB. Other heavy sessions ran at the same time. The 1-minute load average was 12 to 28 during the timings. Builds used `nice -n 10` and `CARGO_BUILD_JOBS=4`.

## Numbers

Contract:

| Item | Result |
|---|---|
| Cookie (ADR 0008 §3) | `session=<22 chars>; HttpOnly; SameSite=Lax; Path=/; Max-Age=604800`, plus `Secure` unless `SPINUP_ENV=dev`. Tests read the `Set-Cookie` header. |
| Logout and reset | a removal cookie with `Max-Age=0`, and the row is gone |
| Stored token | hex SHA-256 of the cookie value, never the value |
| Token rotation | login and registration give a new ID. The old ID answers 401. |
| Go rate-limit tables | all pass with the Go values unchanged |
| `auth_ratelimit.txtar` texts | equal: `AUTH_RATE_LIMIT_LOGIN "burst=0" is not valid`, `Try: set AUTH_RATE_LIMIT_LOGIN=burst=20,every=3s`, `AUTH_TRUSTED_PROXIES item "bad-proxy"`, `Try: use items such as private`, `retry-after: 3600`, `Try again in 3600 seconds` |
| txtar request sequence | equal: 401, 401, 429, 401 (other client), 429 (forged left entry), 401 ×3 (loopback), 401 (session route) |
| 429 without database work | a refused login with a bogus cookie answers 429 on a pool that cannot connect |
| 503 from the hash queue | gives the rate limit token back (the Go case passes) |
| Lockout | 5 failures, then 429 with Retry-After up to 900 s. It ends after the window. |
| Cross-origin table | the 10 Go cases pass |
| argon2id PHC | a Go hash verifies in Rust. Rust writes the same string for the same salt. |

OIDC comparison (same fake issuer, same cases):

| Item | oauth2 5 + jsonwebtoken 11 (kit) | openidconnect 4 |
|---|---|---|
| Token cases (success, nonce, unverified, issuer, audience, expired, missing ID token, signature) | 8 of 8 equal to Go | 8 of 8 equal to Go |
| `email_verified` cases | 11 of 11 equal to Go | 10 of 11: the string `"true"` fails the whole token (needs feature `accept-string-booleans`) |
| HTTP client | reqwest 0.13 through a 20-line adapter | reqwest 0.12 (a second reqwest in the tree) |
| Packages (normal dependencies) | todo-api total 272 | `oidc-compare` alone 155 |
| `rsa` (RUSTSEC-2023-0071) | yes, through the `rust_crypto` backend | yes, always |
| Code | `oidc.rs` 878 lines without tests (steps, routes, cookie, account link) | 108 lines for the steps only (typed claims) |

cargo deny (`advisories bans licenses`, offline): ok. Two advisories are ignored in `deny.toml` with a reason: `RUSTSEC-2023-0071` (rsa) and `RUSTSEC-2026-0387` (crypto-bigint 0.5 through p256 and p384). Both come from `jsonwebtoken` `rust_crypto` and from `openidconnect`. The kit uses them only to verify signatures with public keys. Bans: 19 duplicate versions (warnings) for the app graph.

Build cost of auth (package `todo-api`, clean target directory each time):

| Measurement | R0.6 (no auth) | R0.7 (auth) | Added | Load (1 min) |
|---|---|---|---|---|
| Clean debug build | 39.6 s (user 104 s) | 83.0 s (user 173 s) | +43.4 s | R0.6 12.3, R0.7 27.8 |
| Clean release build | 87.3 s (user 246 s) | 117.9 s (user 372 s) | +30.6 s (user +126 s) | R0.6 15.5 to 27.8, R0.7 27.3 to 21.6 |
| Release binary (not stripped) | 11.3 MB (11 312 144 B) | 18.4 MB (18 396 416 B) | +7.1 MB | |
| Debug binary | 34.7 MB | 55.1 MB | +20.5 MB | |
| Packages (normal, todo-api) | 194 | 272 | +78 | |
| Packages in `Cargo.lock` | 234 | 390 (with oidc-compare) | | |
| Warm rebuild after a handler-body edit (3 runs) | 1.68 s median (R0.6 result) | 1.72, 1.96, 2.63 s | | 20.9 |

The load during the R0.7 debug build was twice the load of the R0.6 debug build, so the debug difference is too high. The user-time difference is a better guide: about +70 s debug and +126 s release. R0.12 measures on an idle machine.

Password hash (default parameters, release build, load 21.3):

| Measurement | Value |
|---|---|
| Go `argon2.IDKey`, 4 lanes in parallel | median 54 ms wall (45 to 70 ms) |
| Rust `argon2` 0.6, lanes on one thread | median 98 ms wall (96 to 135 ms) |
| Rust, 4 threads in parallel | 29 ms wall per hash |
| Rust, unoptimized dev build | about 1 s per hash: two logins moved `Retry-After` from 3600 to 3598 in the txtar replay |

## Verdict

Verdict: go

The cookie contract of ADR 0008 §3 holds, and the tests check the `Set-Cookie` header. The Go rate-limit tables pass with their values unchanged. The error texts equal `auth_ratelimit.txtar`, and the replay against the binary gives the same status codes. The own sqlx `SessionStore` works with tower-sessions. argon2id, the hash queue, the lockout, the cross-origin check, password reset with lettre, and OIDC all work with the Go texts. One OIDC crate is chosen (`oauth2` + `jsonwebtoken`), and `cargo deny` passes with two documented advisory ignores. Auth adds 7.1 MB to the release binary and about 30 to 45 s to a clean build.

## Recommendation

- OIDC: use `oauth2` 5 + `jsonwebtoken` 11, not `openidconnect` 4. It is the only candidate that keeps the Go `email_verified` rule, it uses the reqwest 0.13 of the kit, and the code is ours to read. openidconnect adds reqwest 0.12 and always pulls `rsa`.
- jsonwebtoken backend: use `aws_lc_rs` when the pack lock is made with network access. It drops `rsa` and `crypto-bigint`, so both `deny.toml` ignores go away. It was blocked here: `blocked: untrusted 0.7.1` (aws-lc-rs default feature `ring-sig-verify`, not in the offline cache). Without a backend feature, jsonwebtoken 11 compiles and panics at the first token. Keep a test that verifies a real token.
- RUSTSEC-2023-0071: until the backend changes, keep the `deny.toml` ignore with its reason (verify-only, no RSA private key in the kit). Put the same rule in `rust-plan.md` risk 7.
- Sessions: keep the own store and the `user_id` column. tower-sessions works, with three limits. A save failure answers an empty 500, not a problem. The removal cookie has no `HttpOnly`, `SameSite`, or `Secure`. A session that changes after login falls back to the default expiry of tower-sessions. If R1 needs these to match Go exactly, a small own session layer (about 200 lines) is the fallback. Sessions written by the Go app (gob data) do not carry over, so an upgrade from Go to Rust signs every user out.
- Passwords: the argon2 crate has no `parallel` feature without rayon, so one hash uses one thread: 98 ms against 54 ms in Go, but less CPU. The default slot count in the spike is 40% of the CPUs (Go: 10%, with 4 lanes each). R1 must confirm it with the load test. Add `[profile.dev.package.argon2] opt-level = 3` (and `blake2`) to the generated app: an unoptimized hash takes about 1 s.
- Auth tables: the session table changes to `data jsonb` and `user_id bigint`. The kit tests keep a copy of the auth migrations in `kit/tests/migrations/`, as the Go kit does.
- Mail: lettre works with the SMTP stub and the file transport. A Mailpit run would add a real SMTP server with its own EHLO, 8BITMIME, and quoted-printable handling, and the HTML part. Run it at the R0.9 or R1 gate.
- `ConnectInfo<SocketAddr>` gives the peer address. `App::serve_on` now uses `into_make_service_with_connect_info`. Unix sockets are not covered.
- Not done: the idle timeout option, mail templates and HTML mail, the OpenTelemetry counter `auth.rate_limited` (R0.8 brings OTel, the spike logs instead), the `*_FILE` secrets, and chunked sweeps (the sweep holds the lock for one `retain`). The database `spinup_r07` on 55433 holds the app tables for `cargo sqlx prepare`. The shared cache holds two measurement target directories, `target-R0.7-measure-r0-06-ref-api` and `target-R0.7-measure-r0-07-ref-auth` (about 4 GB), that the main loop can delete.
