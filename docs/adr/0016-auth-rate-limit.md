# ADR 0016: Per-client rate limit for the auth routes

Status: accepted (2026-10-22)

The load test of the todo app showed that one client can use all the CPU of an API replica with login requests. Each login checks one argon2id hash, and one hash takes about 0.16 CPU-seconds. The hash queue (ADR 0008) bounds the damage, but it does not stop one client from using all the places in the queue.

This ADR adds a rate limit for each client to kit `auth`. ADRs 0001 to 0015 still apply. ADR 0008 §3 has a dated amendment for the new 429 answers.

## 1. Where the check runs

- The check is in `Auth.Middleware`. It runs before the cross-origin check, the session load, and every query.
- A refused request makes no database call. A test counts the pool acquires of a flood with a bogus session cookie, and the count is 0.
- One route table (method and exact path, and the OIDC callback by its suffix) is read by `Routes`, `ResetRoutes`, `oidcRoutes`, and the middleware. The patterns of the mux and the check cannot drift apart.
- An app that does not call `Middleware` has no limit, and it also has no working `Routes`.

## 2. Classes and defaults

Each class has its own buckets. A flood of one class does not block another class.

| Class | Route | Burst | Every | Cost of one request |
|---|---|---|---|---|
| `login` | `POST /api/auth/login` | 20 | 3 s | one argon2id check |
| `register` | `POST /api/auth/register` | 20 | 20 s | one argon2id hash and one insert |
| `reset_request` | `POST /api/auth/reset/request` | 10 | 30 s | two advisory-lock transactions and a mail |
| `reset_confirm` | `POST /api/auth/reset/confirm` | 10 | 30 s | one argon2id hash |
| `oidc_callback` | `GET /api/auth/oidc/{provider}/callback` | 100 | 1 s | token exchange and verification |

- Not limited: `POST /api/auth/logout`, `GET /api/session`, `GET /api/auth/oidc/providers`, and `GET /api/auth/oidc/{provider}/start`. The web hooks call `GET /api/session` on every page. The `start` route does AES-GCM and a redirect, and no outbound call.
- The algorithm is GCRA with one `int64` for each bucket. Burst requests pass at once, and then one request each Every.
- A request that passes the check takes one token, also when it later fails validation. A refused request takes no token. A request that the hash queue refuses with 503 gives its tokens back.

## 3. The client address

- `AUTH_TRUSTED_PROXIES` (or `auth.WithTrustedProxies`) lists the peers that may write `X-Forwarded-For`. An item is a CIDR, a bare address, or a word. The default is `private`.
  - `private`: loopback, RFC 1918, `fc00::/7`, `100.64.0.0/10`, `169.254.0.0/16`, and `fe80::/10`.
  - `loopback`: `127.0.0.0/8` and `::1`.
  - `none`: nobody.
- A peer that is not trusted is the client. The API ignores every forwarding header.
- A trusted peer is a proxy. The client is the first `X-Forwarded-For` entry from the right that is not trusted. The API reads at most 16 entries from the right. An entry that does not parse ends the walk.
- A client cannot choose the entry that the nearest proxy writes. Caddy replaces any inbound `X-Forwarded-For` unless `trusted_proxies` is set. The Node hop (`handleFetch`) deletes the inbound value and writes one entry.
- Loopback is exempt when the trusted set holds it. With `none`, local requests are limited too.
- A trusted peer that is not loopback and gives no client address goes into one shared infrastructure bucket for each class. This covers a proxy without the header, an unparsable header, and a header with only trusted entries. The bucket is large and it is never unlimited:
  - Burst 50 for `login`, `register`, `reset_request`, and `reset_confirm`, with Every = 320 ms / GOMAXPROCS (at least 1 ms). This is about twice the hash capacity of the machine.
  - Burst 200 and Every 50 ms for `oidc_callback`.
  - The API writes one `Warn` line at most every 10 minutes when a request uses this bucket. The line has no address.
- IPv4 clients use the exact address. IPv6 clients use two buckets: the /64 and the /48 around it. The /48 bucket has 8 times the budget of the /64 bucket. A request takes one token from each in one lock. When the /48 bucket refuses, the /64 bucket keeps its token. A free /48 gives 65,536 networks, and the /48 bucket keeps the sum of them at 8 times the budget of one.

## 4. Configuration

- The kit reads the environment with `config.LoadFrom` and `os.LookupEnv`. It does not read `.env`. This is the same as the OIDC settings.
- Names:
  - `AUTH_RATE_LIMIT`: `on` (default) or `off`. `off` turns every class off.
  - `AUTH_RATE_LIMIT_LOGIN`, `_REGISTER`, `_RESET_REQUEST`, `_RESET_CONFIRM`, `_OIDC_CALLBACK`: `off`, or `burst=<n>,every=<duration>` with each key optional. Burst is 1 to 10000. Every is 1 ms to 24 h. Burst times Every is at most 7 days.
  - `AUTH_RATE_LIMIT_CLIENTS`: the maximum number of entries (all classes), 1024 to 4194304. The default is 65536, about 5 MB.
  - `AUTH_TRUSTED_PROXIES`: the list of section 3.
- Go API: `auth.RateLimits` (a `Limit{Burst, Every, Off}` for each class and `Clients`), `auth.WithRateLimits`, and `auth.WithTrustedProxies`.
- Precedence: the code option wins over the environment, and the environment wins over the default. A non-zero Burst or Every in a code option turns the class on, also when `AUTH_RATE_LIMIT=off`.
- A value that does not parse stops the start. The error names the variable and ends with a `Try:` line.
- The table never holds more than Clients entries. A sweep each minute removes the entries whose bucket is full, in chunks. At the limit, an insert removes a full bucket first, and otherwise the entry with the shortest penalty left in a sample of 16.

## 5. The 429 answer

- Problem JSON with `Retry-After` (whole seconds, at least 1) and `Cache-Control: no-store`. The `detail` ends with `Try again in N seconds.` and names the class: sign-in attempts, sign-up attempts, or password reset requests.
- The text has no address and no key.
- The `login` route also answers 429 for the lockout of one email. The status and `type` are the same, and the `detail` text differs.
- The OIDC callback is a browser navigation, so a JSON page would end the flow. A refused callback answers `303` to `/login?error=rate_limited` with `Retry-After`. It does not clear the flow cookie. The login page of the auth pack does not read the `error` parameter yet.
- The API writes one `Warn` line each minute with the refusals for each class, never an address. It counts them in the OpenTelemetry counter `auth.rate_limited` with the attribute `class`.

## 6. Web and deploy

- `handleFetch` in `hooks.server.ts` deletes any inbound `X-Forwarded-For` on API requests. It sets `X-Forwarded-For` to `event.getClientAddress()` only when `ADDRESS_HEADER` is set. Node is the TCP peer of the API for form actions, so without this every web user would share the address of Node.
- The generated `compose.yml` sets `ADDRESS_HEADER=X-Forwarded-For` on the web service. An old `compose.yml` with a new hook, or the reverse, does not lock anybody out: the API sees no client address from Node, and the infrastructure bucket applies.

## 7. Replicas and limits of this design

- The state is in the memory of each API process. With N replicas a client can pass about N times the numbers. The CPU share of the client for each replica stays the same. A global limit needs a shared store, and the kit does not add one. Use a firewall or a CDN for an exact limit.
- The limit does not stop a flood of cheap requests. A refused request still costs the access log and the response. A firewall or a CDN stops that flood.
- The limit does not prevent a targeted lockout. An attacker who knows an email can lock it with 5 failed logins from one client.
- A CDN in front of Caddy needs three changes: `trusted_proxies` in Caddy with the CDN ranges, the CDN ranges added to `AUTH_TRUSTED_PROXIES`, and `XFF_DEPTH=2` on the web service.
- A proxy with a public address that `AUTH_TRUSTED_PROXIES` does not list makes all users share one bucket.

## 8. Follow-ups

- Trust the peer inside a subnet of a local network interface, so that any Docker address pool works.
- Detect one key that carries almost all requests, and log an error with a `Try:` line.
- A `spinup doctor` check for `X-Forwarded-For` in `hooks.server.ts` and `ADDRESS_HEADER` in `compose.yml`.
- A dind test with forged `X-Forwarded-For` values through Caddy, and one with an IPv6 client.
- A dashboard panel and an alert for `auth.rate_limited`.
- `kit/deployx` keeps its own check (`IsPrivate` or `IsLoopback`). The `private` word here is wider (it adds carrier-grade NAT and link-local ranges), so a shared definition would change who counts as internal for `/_deploy/info`.
- The login page of the auth pack can show the message for `?error=rate_limited`.
