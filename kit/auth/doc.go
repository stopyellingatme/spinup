// Package auth handles users, passwords, and sessions for a spinup API.
//
// [New] builds an [Auth] on a PostgreSQL pool. The pool must hold the tables of
// the auth pack migration: users, sessions, and login_attempts.
//
//   - Sessions use scs with the pgxstore store (table sessions). The store holds a
//     SHA-256 hash of each token, not the token. The cookie is named "session" and
//     is HttpOnly, SameSite=Lax, Path=/, and Secure unless [WithSecureCookie] turns
//     Secure off. Login and registration rotate the token. Logout destroys the
//     session in the store and clears the cookie.
//   - Passwords are hashed with argon2id in PHC string format. The defaults follow
//     the second recommended option of RFC 9106: 64 MiB of memory, 3 passes, and
//     4 lanes, with a 16-byte salt and a 32-byte key. A login for an unknown email
//     verifies a dummy hash, so the response time does not reveal accounts.
//   - After 5 failed logins for one email within 15 minutes, a login for that email
//     gets 429 with Retry-After. A successful login clears the count.
//   - [Auth.Middleware] rejects unsafe cross-origin requests with 403 (see
//     [http.CrossOriginProtection]) and sets a default Content-Security-Policy.
//
// Wire it into kit app:
//
//	authn, err := auth.New(pool, auth.WithSecureCookie(cfg.Env != "dev"))
//	...
//	app.New(
//		app.WithMiddleware(authn.Middleware()),
//		app.WithRoutes(authn.Routes),
//		app.WithShutdown(authn.Close),
//	)
//
// Protect a handler with [Auth.RequireUser] and read the user with [UserFrom].
package auth
