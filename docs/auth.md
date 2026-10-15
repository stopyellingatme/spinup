# Accounts and sessions (`auth` module)

The `auth` module adds user accounts to an app.
Users can register, log in, and log out.
The Go API owns the session. The web app shows the login pages and reads the session on the server.

The module needs the web app, so you cannot use it with `--api-only`.

```console
$ spinup new todo --module example.com/todo --modules ci,auth --no-git --non-interactive
...
$ cd todo
$ ls web/src/routes
+layout.server.ts
+layout.svelte
+page.svelte
account
healthz
login
logout
register
$ ls api/db/migrations
00001_init.sql
00002_create_auth.sql
embed.go
```

An API-only app fails with an error:

```console
$ spinup new apionly --api-only --module example.com/apionly --modules auth --no-git --non-interactive
Error: pack "auth" requires missing pack "web"
  Try: select the required module with --modules
[exit 1]
```

## Files

| Path | Class | Purpose |
| --- | --- | --- |
| `api/db/migrations/00002_create_auth.sql` | seed | The tables `users`, `sessions`, and `login_attempts`. |
| `api/cmd/api/main.go` | scaffold | The api pack adds kit `auth` to this file when the module is on. |
| `web/src/lib/server/auth.ts` | managed | `postAuth()` and `forwardSessionCookie()`: form actions use them to call the API. |
| `web/src/lib/server/auth.test.ts` | managed | The vitest tests of the helper. |
| `web/src/routes/login/` | scaffold | The login page and its form action. |
| `web/src/routes/register/` | scaffold | The registration page and its form action. |
| `web/src/routes/logout/+page.server.ts` | scaffold | The logout action. It has no page. A `GET /logout` goes to `/`. |
| `web/src/routes/account/` | scaffold | Shows the email of the user. Without a user, it goes to `/login`. |

The API main file creates kit `auth` and adds its middleware and routes:

```console
$ grep authn api/cmd/api/main.go
	authn, err := auth.New(pool, auth.WithSecureCookie(cfg.Env != "dev"), auth.WithTrustedOrigins(web.PublicOrigin))
		app.WithMiddleware(authn.Middleware()),
		app.WithRoutes(authn.Routes),
		app.WithShutdown(authn.Close),
```

## The API

| Route | Body | Success | Errors |
| --- | --- | --- | --- |
| `POST /api/auth/register` | `{"email","password"}` | 201 `{"user":{"id","email"}}` and the session cookie | 409 (email taken), 422 |
| `POST /api/auth/login` | `{"email","password"}` | 200 `{"user":{"id","email"}}` and a new session cookie | 401, 422, 429 |
| `POST /api/auth/logout` | none | 204, and the cookie is cleared | |
| `GET /api/session` | none | 200 `{"user":{"id","email"}}` | 401 |

- Errors are problem details (`application/problem+json`).
- A 422 problem has an `errors` list with the fields `email` and `password`.
- The register and login routes accept only `application/json` bodies, up to 16 KiB.
- The auth responses have `Cache-Control: no-store`.

Try the API with `curl` while the API runs on port 8080:

<!-- doctest: skip -->
```console
$ curl -c jar -H 'Content-Type: application/json' -d '{"email":"ada@example.com","password":"correct horse battery"}' http://localhost:8080/api/auth/register
{"user":{"id":1,"email":"ada@example.com"}}
$ curl -b jar http://localhost:8080/api/session
{"user":{"id":1,"email":"ada@example.com"}}
```

### Protect a route

Wrap a handler with `RequireUser`. Read the user with `auth.UserFrom`:

```go
mux.Handle("GET /api/todos", authn.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFrom(r.Context())
	// Load the todos of user.ID.
})))
```

`RequireUser` answers 401 without a user.

## Security defaults

### Sessions

- The session cookie is `session`. It is `HttpOnly`, `SameSite=Lax`, and `Path=/`.
- The cookie is `Secure` unless `SPINUP_ENV=dev`. In production, set `SPINUP_ENV` to a value other than `dev`.
- The sessions are in the `sessions` table (scs with the pgxstore store). The table holds a SHA-256 hash of each token, not the token.
- A session lives for 7 days. A new login starts a new lifetime. `auth.WithLifetime` changes the lifetime.
- There is no idle timeout by default. `auth.WithIdleTimeout` adds one. With an idle timeout, the browser cookie can expire before the server session, because server-side calls do not refresh the browser cookie.
- Login and registration rotate the session token. The old token stops working.
- Logout deletes the session from the store and expires the cookie.
- A session of a deleted user ends at its next request.
- A goroutine deletes expired sessions and old login attempts every 5 minutes. `authn.Close` stops it.

### Passwords

- A password has 12 to 512 characters (Unicode code points).
- kit `auth` hashes passwords with argon2id and stores PHC strings, for example `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>`.
- The parameters follow the second recommended option of RFC 9106: 64 MiB of memory, 3 passes, and 4 lanes. The salt has 16 random bytes. The key has 32 bytes.
- The comparison runs in constant time.
- If the parameters of a stored hash differ from the current parameters, a successful login stores a new hash.
- The number of hashes that run at the same time is limited to `GOMAXPROCS` (at least 2). Thus a burst of logins cannot use all the memory.
- A login for an unknown email checks a dummy hash, so the response time does not show which emails have accounts.

### Emails

kit `auth` removes the spaces around an email and changes it to lower case.
The result must be a bare address that `net/mail` accepts, with a maximum of 254 characters.

### Lockout

- After 5 failed logins for one email in 15 minutes, logins for that email get 429 with `Retry-After`.
- The lock also applies to emails without an account, so it does not show which emails have accounts.
- The lock opens when the oldest of the 5 failures is 15 minutes old.
- A successful login clears the failures of the email.
- `auth.WithLockout(threshold, window)` changes the values.

An attacker who knows an email can lock it. Add rate limits by IP address at the proxy if this is a risk.

### Cross-site requests

`authn.Middleware()` uses `http.CrossOriginProtection` of the Go standard library.
It rejects a request with 403 when all of these conditions are true:

- The method is not `GET`, `HEAD`, or `OPTIONS`.
- The browser sends `Sec-Fetch-Site: cross-site` or `same-site`, or the `Origin` header is different from the `Host` header.
- The `Origin` is not a trusted origin.

SvelteKit form actions call the API on `API_URL`, with the `Origin` of the web app.
Thus `main.go` trusts `PUBLIC_ORIGIN` (default `http://localhost:5173`).
Give the API the same `PUBLIC_ORIGIN` as the web build.
If the values are different, the API rejects all form actions with 403.

### Content Security Policy

The middleware sets `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` on API responses that have no policy.

## The web pages

The form actions post to the API with the `fetch` of the action.
`handleFetch` in `hooks.server.ts` sends these requests to `API_URL` with the browser cookie.
The `Set-Cookie` header of a server-side fetch does not go to the browser by itself.
Thus `forwardSessionCookie()` reads `res.headers.getSetCookie()` and calls `cookies.set()` with the same attributes (ADR 0002 rule 7).

- A successful login or registration goes to `/`.
- A failure shows the field errors and keeps the email. The password is never sent back to the page.
- On every request, `hooks.server.ts` reads the user from `GET /api/session`. Thus `/account` renders the email on the server.
- SvelteKit checks the `Origin` of form posts in production builds. A post without an `Origin` gets 403.

Run the flow with `curl` against a production build. Send the `Origin` and `Accept` headers as a browser does:

<!-- doctest: skip -->
```console
$ curl -c jar -b jar -H 'Origin: http://localhost:3000' -H 'Accept: text/html' --data-urlencode 'email=ada@example.com' --data-urlencode 'password=correct horse battery' -o /dev/null -w '%{http_code} %{redirect_url}\n' http://localhost:3000/login
303 http://localhost:3000/
$ curl -b jar -s http://localhost:3000/account | grep -o 'ada@example.com' | head -1
ada@example.com
```

Run the tests of the helper in `web/`:

<!-- doctest: skip -->
```console
$ pnpm test
```
