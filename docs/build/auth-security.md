---
title: Sign-in security
parent: Build your app
nav_order: 5
---

# Sign-in security

The `auth` module is safe by default. This page lists the defaults and shows how to change them.
The most common change is to set the rate limit or the trusted proxies for a deployed app.

For a public site behind Caddy, you usually change nothing.
For an office or a CDN, add the variables to the `environment` of the `api` service in `deploy/compose.yml`:

```yaml
services:
  api:
    environment:
      AUTH_RATE_LIMIT_LOGIN: burst=60,every=1s
      AUTH_TRUSTED_PROXIES: private,203.0.113.0/24
```

The generated `api` service has an explicit list of variables, so the shell of the VM does not reach it.
Deploy again after you change a variable.

{: .note }
Edit `deploy/compose.yml` itself. If you define the `api` service again in `deploy/compose.override.yml`, Compose stops with `services.api conflicts with imported resource`. The file `compose.yml` is managed, so `spinup upgrade` can show a conflict near your lines. Keep your lines when you resolve it.

Settings for sessions, lockout, and hashing are Go options in `api/cmd/api/main.go`:

```go
authn, err := auth.New(pool,
	auth.WithLifetime(24*time.Hour),
	auth.WithLockout(10, 30*time.Minute),
	// ... the options that the template wrote
)
```

## Sessions

| Property | Value | Change it with |
| --- | --- | --- |
| Cookie name | `session` | |
| Cookie flags | `HttpOnly`, `SameSite=Lax`, `Path=/`, and `Secure` unless `SPINUP_ENV=dev` | |
| Store | The `sessions` table. It holds a SHA-256 hash of each token, not the token. | |
| Lifetime | 7 days. A new sign-in starts a new lifetime. | `auth.WithLifetime(d)` |
| Idle timeout | None | `auth.WithIdleTimeout(d)` |

- Sign-in and registration rotate the session token. The old token stops working.
- Sign-out deletes the session from the store and expires the cookie.
- The session of a deleted user ends at its next request.
- A background task deletes expired sessions and old sign-in attempts every 5 minutes.
- With an idle timeout, the browser cookie can expire before the server session, because server-side calls do not refresh the browser cookie.

If `SPINUP_ENV` is not set, the API assumes `production`. Cookies stay `Secure`, and the `/_dev` routes stay off.
The generated `Dockerfile.api` sets `SPINUP_ENV=production`. `spinup dev` and `.env.example` select `dev`.

## Passwords

A password has 12 to 512 characters.
The kit hashes passwords with argon2id and stores PHC strings, for example `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>`.

| Setting | Value |
| --- | --- |
| Algorithm | argon2id (RFC 9106, second recommended option) |
| Memory | 64 MiB |
| Passes | 3 |
| Lanes | 4 |
| Salt | 16 random bytes |
| Key | 32 bytes |

- The comparison runs in constant time.
- If the parameters of a stored hash differ from the current parameters, a successful sign-in stores a new hash.
- A sign-in for an unknown email checks a dummy hash, so the response time does not show which emails have accounts.
- The kit removes the spaces around an email and changes it to lower case. The result must be a bare address of 254 characters or fewer.

### Hash limits

A hash is costly. So the API limits how many run at the same time, and a burst of sign-ins cannot starve the other routes.

| Setting | Value |
| --- | --- |
| Hashes at once | 1 for each 10 CPUs of `GOMAXPROCS` (at least 1) |
| Waiting requests | 4 for each slot, for 2 seconds at most |
| A full queue | 503 with `Retry-After: 1` |

A sign-in that gets 503 checked no password, so it does not count toward the lockout.
To check more passwords each second, raise the limits with `auth.WithHashLimits(concurrent, waiting, wait)`, or run more API replicas.

## Lockout

After 5 failed sign-ins for one email in 15 minutes, sign-ins for that email get 429 with `Retry-After`.

- A successful sign-in clears the failures of the email.
- The lock opens when the oldest of the 5 failures is 15 minutes old.
- The lockout also applies to emails without an account, so it does not show which emails have accounts.
- `auth.WithLockout(threshold, window)` changes the values.

{: .note }
A person who knows an email can lock it. The rate limit for each client does not prevent this. A captcha or an unlock link can reduce the risk. The kit does not have them.

## Rate limits

The kit limits the costly requests for each client.
A client over its limit gets 429 with `Retry-After`, and the request does not touch the database.
The detail text says how long to wait, for example `Too many sign-in attempts from your network. Try again in 12 seconds.`
The sign-in and registration forms show this text.

Each class has its own limit. The burst passes at once. After that, one request passes in each "Every" period.

| Class | Route | Burst | Every |
| --- | --- | --- | --- |
| `login` | `POST /api/auth/login` | 20 | 3 s |
| `register` | `POST /api/auth/register` | 20 | 20 s |
| `reset_request` | `POST /api/auth/reset/request` | 10 | 30 s |
| `reset_confirm` | `POST /api/auth/reset/confirm` | 10 | 30 s |
| `oidc_callback` | `GET /api/auth/oidc/<provider>/callback` | 100 | 1 s |

Sign-out, `GET /api/session`, the provider list, and the OIDC start route have no limit.
A refused OIDC callback redirects to `/login?error=rate_limited`.

### Change a limit

Set these variables in the environment of the API.
The kit does not read them from a `.env` file.

| Variable | Value | Default | Example |
| --- | --- | --- | --- |
| `AUTH_RATE_LIMIT` | `on` or `off`. `off` turns every class off. | `on` | `off` |
| `AUTH_RATE_LIMIT_LOGIN`, `_REGISTER`, `_RESET_REQUEST`, `_RESET_CONFIRM`, `_OIDC_CALLBACK` | `off`, or `burst=<n>,every=<duration>`. Each key is optional. | The table above | `burst=60,every=1s` |
| `AUTH_RATE_LIMIT_CLIENTS` | How many clients to keep in memory, 1024 to 4194304. | `65536` (about 5 MB) | `131072` |
| `AUTH_TRUSTED_PROXIES` | A comma list of CIDRs, addresses, and the words `private`, `loopback`, and `none`. | `private` | `private,203.0.113.0/24` |

- `private` is loopback, RFC 1918, `fc00::/7`, `100.64.0.0/10`, `169.254.0.0/16`, and `fe80::/10`.
- A value that does not parse stops the API at startup. The error names the variable and has a `Try:` line.
- In code, `auth.WithRateLimits(auth.RateLimits{Login: auth.Limit{Burst: 60, Every: time.Second}})` and `auth.WithTrustedProxies("private", "203.0.113.0/24")` set the same values. A value in code wins over the environment.
- A non-zero `Burst` or `Every` in code turns the class on again, also when `AUTH_RATE_LIMIT=off`.

Many users can share one address, for example an office or a mobile network.
With the default `login` limit, one address passes 20 sign-ins at once and then 20 each minute.
A session lasts 7 days, so a user signs in about once each week. If a site needs more, raise the limit.
Each allowed sign-in costs CPU time, so do not remove the limit on a public site.

### Which address is the client

The limit is for each client address. The API must find the true address behind your proxy.

| Case | The client is |
| --- | --- |
| The TCP peer is not trusted | The peer. The API ignores `X-Forwarded-For`. |
| The TCP peer is trusted | The first `X-Forwarded-For` entry from the right that is not trusted. A client cannot choose this entry. |
| The peer is loopback | Not limited. `spinup dev`, tests, and health checks use loopback. |

- Caddy replaces `X-Forwarded-For` with the address of its peer. The web service sends the client address to the API in the same header (`ADDRESS_HEADER=X-Forwarded-For` in `deploy/compose.yml`).
- An IPv4 client has its own bucket. An IPv6 client has a bucket for its /64 network and a bucket for its /48 network. The /48 bucket has 8 times the budget.
- A trusted proxy that does not tell the client address goes into one shared bucket for each class. The API writes a warning to the log when this happens.
- An intranet app has private client addresses. Set `AUTH_TRUSTED_PROXIES=172.16.0.0/12` to trust only the Docker networks.

### Put a CDN in front of Caddy

A CDN sends the client address in a header, and Caddy adds the address of the CDN.

1. Set `trusted_proxies` in the global options of `deploy/Caddyfile` to the CDN ranges.
2. Add the CDN ranges to `AUTH_TRUSTED_PROXIES` of the `api` service, for example `private,<CDN ranges>`.
3. Change `XFF_DEPTH` of the `web` service from `"1"` to `"2"`.

Steps 2 and 3 edit `deploy/compose.yml`. [Deploy to your own server](../deploy.md) shows the Caddy block.

### Limits of the rate limit

- Each API process has its own counters. With 2 replicas, a client can pass about twice the numbers. Use a firewall or a CDN for an exact global limit.
- The limit stops costly work, not a flood of cheap requests. Use a firewall or a CDN to stop that flood.
- The counters are empty after a restart.
- A proxy with a public address that is not in `AUTH_TRUSTED_PROXIES` makes all users share one bucket. Add the proxy to the list.
- The API trusts a private address that sends `X-Forwarded-For`. Do not publish the API port. If you must, set `AUTH_TRUSTED_PROXIES` to the proxy addresses only.
- The generated Docker network has no IPv6. Docker then shows an IPv6 client to Caddy as the address of the bridge gateway, and all IPv6 clients share one bucket. spinup does not test an IPv6 setup yet.
- Some providers give each customer a /56 network inside a shared /48 network. One customer can then empty the /48 bucket for the others.
- The API logs one warning each minute with the number of refused requests for each class. The counter `auth.rate_limited` has the attribute `class`. The log never has an address.

## Cross-site requests

`authn.Middleware()` uses `http.CrossOriginProtection` of the Go standard library.
It rejects a request with 403 when all of these are true:

- The method is not `GET`, `HEAD`, or `OPTIONS`.
- The browser sends `Sec-Fetch-Site: cross-site` or `same-site`, or the `Origin` header differs from the `Host` header.
- The `Origin` is not a trusted origin.

SvelteKit form actions call the API on `API_URL`, with the `Origin` of the web app.
So `main.go` trusts `PUBLIC_ORIGIN` (default `http://localhost:5173`).
Give the API the same `PUBLIC_ORIGIN` as the web build.
If the values differ, the API rejects all form actions with 403.

The middleware also sets `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` on API responses that have no policy.

## Account events

With the [audit trail](../audit.md), the auth module records account events.
Without the audit module, the auth module records no event.
A new app with `--modules auth,audit` has the wiring in `api/cmd/api/main.go`.

```console
$ spinup new todo --module example.com/todo --modules auth,audit --no-git --non-interactive
...
$ grep -E 'WithEvents|WithActorContext' todo/api/cmd/api/main.go
		auth.WithEvents(authsink.New(pool)),
		auth.WithActorContext(authsink.ActorContext),
```

`auth.WithEvents` sets the event sink.
`auth.WithActorContext` stores the signed-in user as the audit actor, so a handler reaches `audit.InTx` with that actor.

| Event | When | If the sink fails |
| --- | --- | --- |
| `register` | A new account, through the register route or the first OIDC sign-in | The insert fails |
| `user_create` | `user create` of the operator command | The insert fails |
| `role_change` | `user set-role` and `user backfill` | The change fails |
| `password_reset` | A new password through a reset token | The change fails |
| `password_rehash` | A new hash of the same password, with new parameters | The change fails |
| `sign_in` | A successful sign-in, with the method `password` or `oidc` | The kit logs the error, and the user signs in |
| `sign_out` | A sign-out | The kit logs the error, and the user signs out |
| `lockout` | The start of a lockout of an existing account | The kit logs the error |

- The first five events are row changes of `users`. In `audit list`, they show as `create` and `update`.
- `sign_in`, `sign_out`, and `lockout` show as the actions `auth.sign_in`, `auth.sign_out`, and `auth.lockout` on the table `users`.
- The actor of `register` is the new account, with the reason `register`. The actor of `user_create` and `role_change` is `operator:<login>`. Add `--reason` to record why.
- A failed sign-in for an email that has no account writes nothing. A `lockout` event is written once for each lockout.
- The trigger never stores `password_hash`. The event shows `changed (value hidden)`.

Read the events with the operator command:

<!-- doctest: skip -->
```console
$ go run ./cmd/api audit list --resource users --action auth.sign_in --since 2026-11-01
$ go run ./cmd/api audit list --resource users --actor operator:ada
```

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| Users behind one office address get 429 | They share one client bucket. | Raise `AUTH_RATE_LIMIT_LOGIN`. |
| Every user gets 429 behind a proxy | The proxy is not trusted, so all users share its address. | Add the proxy to `AUTH_TRUSTED_PROXIES`. |
| The API fails to start with a message about a variable | The value does not parse. | Read the `Try:` line of the error. |
| A user cannot sign in for 15 minutes | Five failed sign-ins locked the email. | Wait, or have the user reset the password. |
| Sign-in answers 503 under load | The hash queue is full. | Raise `auth.WithHashLimits`, or add API replicas. |

## See also

- [Accounts and sign-in](../auth.md)
- [Sign in with an identity provider](oidc.md)
- [Audit trail](../audit.md)
- [Deploy to your own server](../deploy.md) for Caddy and the Compose files
