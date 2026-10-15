# Mail and password reset

The `mail` module provides typed mailers and embedded templates.
The kit provides SMTP, log, and capture transports.
Generate a mailer with this command:

```console
$ spinup new letters --api-only --module example.com/letters --modules ci,mail --no-git --non-interactive
...
$ cd letters
$ spinup g mailer welcome name:string
Created 4, updated 0 files
Next: cd api && go test ./internal/mail/...
```

Set `SMTP_URL` for production delivery.
Use `smtp+starttls://user@host:587` for required STARTTLS.
Use `smtps://user@host:465` for implicit TLS.
Authenticated `smtp://user@host:587` also requires STARTTLS.
Neither TLS mode permits a plaintext fallback.

Set the password with `SMTP_PASSWORD` or `SMTP_PASSWORD_FILE`.
Do not put passwords in the URL.
Set `SMTP_TIMEOUT` to limit delivery time.
The default timeout is `10s`.
Set `MAIL_FROM` to the sender address.
The default sender is `noreply@example.com`.
Kit `config.Load` reads these settings and secret files.

In development, the generated mailers use `smtp://localhost:1025` when `SMTP_URL` is absent.
`spinup dev` starts Mailpit.
Open `http://localhost:8025` to inspect the emails.
The kit selects its log transport when its SMTP URL is empty.
The log transport records delivery metadata and omits message content.
Use `mail.Capture` and `Messages()` in tests.

Each mailer has `<name>.subject.tmpl`, `<name>.txt.tmpl`, and `<name>.html.tmpl` under `api/internal/mail/templates/`.
The loader uses `text/template` for the subject and text.
The loader uses `html/template` for HTML escaping.
The loader requires a text part and rejects line breaks in subjects.

With the `jobs` module enabled, mailers insert `mail.send` jobs into the River `mail` queue.
River retries delivery failures with up to ten attempts.
Run the migrations before the worker.
Run the API binary with `mail-worker` to process this queue:

```sh
cd api
go run ./cmd/api migrate up
go run ./cmd/api mail-worker
```

The mail worker consumes only the `mail` queue.
The application worker continues to consume its configured queues.
Protect River storage because rendered messages can contain reset links.

With `auth` and `mail`, the application includes password reset routes and pages.
`POST /api/auth/reset/request` accepts an email address and returns 202.
The request remains identical for unknown accounts, invalid emails, and throttled requests.
The server limits requests by email and the direct peer IP through PostgreSQL.
It ignores untrusted forwarding headers.
Account lookup and email delivery run in a bounded background queue.

`POST /api/auth/reset/confirm` accepts a token and a new password.
Tokens contain 32 random bytes.
The database stores only their SHA-256 hashes.
Tokens expire after one hour and permit one confirmation.
Confirmation applies the registration password limits.
It also invalidates all reset tokens and sessions for that user, and clears login lockout.
The email never contains the password.

The pages are `/reset` and `/reset/[token]`.
Reset links use `PUBLIC_ORIGIN`, which the application controls.
The reset page disables caching and referrer disclosure.

For direct kit integration, call `Auth.ResetRoutes` with `auth.WithMailer`, `auth.WithResetURL`, and `auth.WithResetFrom`.
Install the existing auth middleware on these routes.
Use `auth.WithResetTemplates` for custom templates.
Without a mailer option, `ResetRoutes` registers no routes.
