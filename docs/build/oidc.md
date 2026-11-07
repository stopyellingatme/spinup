---
title: Sign in with an identity provider
parent: Build your app
nav_order: 6
---

# Sign in with an identity provider

OpenID Connect (OIDC) lets users sign in with Google, Keycloak, or another provider.
The sign-in page then shows one button for each provider.
Turn it on with `--auth-oidc` when you create the app.

To add it to an app that has no `auth` module yet, run `spinup module add auth --auth-oidc`. It writes the changes on the branch `spinup/module-auth`. Merge that branch.
If the app already has the `auth` module without OIDC, `spinup module add auth --auth-oidc` fails with `answer module question "auth_oidc": it does not belong to a new pack`. Today, only a new `auth` module takes the OIDC answer, so choose it when you add `auth`.

```console
$ spinup new todo --module example.com/todo --modules auth --auth-oidc --no-git --non-interactive
...
$ cd todo
$ ls api/db/migrations api/db/kit
api/db/kit:
00002_create_auth.sql
00004_oidc_identities.sql
00005_users_role.sql
00007_users_auth_version.sql
embed.go

api/db/migrations:
00001_init.sql
embed.go
$ grep OidcButtons web/src/routes/login/+page.svelte
	import OidcButtons from '#lib/components/OidcButtons.svelte';
		<OidcButtons next={data.redirectTo} />
```

The `auth_oidc` question defaults to false.
The kit migration `00004` in `api/db/kit/` lets a user have no password, and it creates the table `user_identities`.
It follows `00003_password_resets.sql`, which exists only when the mail module is on.
Existing migrations stay unchanged.

You need the [auth module](../auth.md) first.

## Configure a provider

Add the provider settings to the environment of the API.
This example has two providers, `google` and `acme`:

```sh
PUBLIC_ORIGIN=https://app.example.com
AUTH_OIDC_PROVIDERS=google,acme
AUTH_OIDC_GOOGLE_ISSUER=https://accounts.google.com
AUTH_OIDC_GOOGLE_CLIENT_ID=your-client-id
AUTH_OIDC_GOOGLE_CLIENT_SECRET_FILE=/run/secrets/AUTH_OIDC_GOOGLE_CLIENT_SECRET
AUTH_OIDC_GOOGLE_LABEL=Google
AUTH_OIDC_ACME_ISSUER=https://identity.example.com/realms/acme
AUTH_OIDC_ACME_CLIENT_ID=your-client-id
AUTH_OIDC_ACME_CLIENT_SECRET_FILE=/run/secrets/AUTH_OIDC_ACME_CLIENT_SECRET
AUTH_OIDC_ACME_LABEL=Acme
AUTH_OIDC_ACME_SCOPES=groups,offline_access
```

The API reads the provider settings when it registers the routes.
Restart the API after you change them.

The API reads its process environment, not a `.env` file.
In development, export the variables in your shell before you run `spinup dev`.
Use `AUTH_OIDC_GOOGLE_CLIENT_SECRET=<secret>` in place of the `_FILE` form.

### Deploy the settings

For a deployed app, store each client secret with [spinup secrets](../deploy/secrets.md).
A deploy puts the secret in `/run/secrets/<NAME>` of the container.
Then add the other variables to the `environment` of the `api` service in `deploy/compose.yml`:

<!-- doctest: skip -->
```console
$ printf '%s' 'the-client-secret' | spinup secrets set --env production AUTH_OIDC_GOOGLE_CLIENT_SECRET
```

```yaml
services:
  api:
    environment:
      AUTH_OIDC_PROVIDERS: google
      AUTH_OIDC_GOOGLE_ISSUER: https://accounts.google.com
      AUTH_OIDC_GOOGLE_CLIENT_ID: your-client-id
      AUTH_OIDC_GOOGLE_CLIENT_SECRET_FILE: /run/secrets/AUTH_OIDC_GOOGLE_CLIENT_SECRET
```

{: .note }
Edit `deploy/compose.yml`, not `deploy/compose.override.yml`. Compose rejects a second `api` service. See [Deploy to your own server](../deploy.md).

Register this callback URL with each provider. Use the provider name for `<provider>`:

```text
https://app.example.com/api/auth/oidc/<provider>/callback
```

For the example above, the callback of Google is `https://app.example.com/api/auth/oidc/google/callback`.

## Settings

Replace `<NAME>` with the provider name in upper case, for example `GOOGLE`.

| Variable | Meaning | Required |
| --- | --- | --- |
| `AUTH_OIDC_PROVIDERS` | A comma list of provider names. A name has lower-case letters, digits, or underscores, and starts with a letter. | Yes |
| `AUTH_OIDC_<NAME>_ISSUER` | The issuer URL of the provider. | Yes |
| `AUTH_OIDC_<NAME>_CLIENT_ID` | The client ID. | Yes |
| `AUTH_OIDC_<NAME>_CLIENT_SECRET` or `_CLIENT_SECRET_FILE` | The client secret, or the path of a file that holds it. Do not set both. | Yes |
| `AUTH_OIDC_<NAME>_LABEL` | The text of the button. The default is the provider name. | No |
| `AUTH_OIDC_<NAME>_SCOPES` | More scopes, separated by commas or spaces. The scopes `openid`, `email`, and `profile` are always on. | No |
| `AUTH_OIDC_COOKIE_KEY` | The key for the flow cookie. 32 random bytes as unpadded base64url. The `_FILE` form works too. | For more than one API instance |
| `PUBLIC_ORIGIN` | The public origin of the app. Production requires HTTPS. | Yes |

The callback origin comes from `PUBLIC_ORIGIN`, never from request headers.

Without `AUTH_OIDC_COOKIE_KEY`, each process creates a key at startup.
A restart or a key change then ends the sign-in flows that are in progress.
With several API instances, give every instance the same key.

## What happens when a user clicks the button

1. The login page reads the names and labels from `GET /api/auth/oidc/providers`. The route never returns secrets.
2. The button opens `GET /api/auth/oidc/<provider>/start`.
3. The provider asks the user to sign in.
4. The provider sends the user to the callback. The API checks the ID token, then signs the user in.

The flow uses PKCE with S256, a state value, and a nonce.
An encrypted cookie holds the flow for ten minutes. It is `HttpOnly` and `SameSite=Lax`, and `Secure` when the session cookie is `Secure`.
The callback checks the signature, issuer, audience, expiry, and nonce of the ID token.
It then uses the normal session method, which rotates the token.

The API finds the provider on the first attempt and caches the result.
If the provider is down, the API returns a 503 problem with a `Try:` line.
An unavailable provider does not stop the API from starting, and password sign-in keeps working.

### Choose the page after sign-in

The buttons send `?next=<destination>` with the checked `redirectTo` value, or `home`.
For a direct API request, use `?next=/account`.
The destination must start with one slash and stay on the same origin.
The API rejects absolute URLs, protocol-relative URLs, and backslashes.
Without `next`, the destination is `/account`.

## Accounts and linking

The API requires a verified email.
The claim `email_verified` must be the boolean `true` or the string `"true"`.

| Case | Result |
| --- | --- |
| The identity is known | The user signs in to the same account, even when the provider changed the email. |
| The identity is new, and an account has the same email (after the API makes it lower case) | The identity links to that account. |
| The identity is new, and no account matches | The API creates an account with no password. It follows the [registration mode](../auth.md#choose-who-can-register). |

{: .warning }
Linking a provider to an existing account removes the password of the account and signs out its other sessions. The link increments the auth version of the account, so every older session ends at its next request. The user can set a password again with password reset, which needs the [mail module](../mail.md).

Password sign-in rejects an account with no password, with the usual generic error.
A new account gets `default_role`. See [Roles](roles.md).

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| The button does not appear | The provider is not in `AUTH_OIDC_PROVIDERS`, or the API was not restarted. | Check the name and restart the API. |
| The provider shows `redirect_uri_mismatch` | The callback URL at the provider differs from the one above. | Register the exact callback URL. |
| The button leads to a 503 problem | The API cannot reach the issuer. All requests share one discovery request, which stops after 15 seconds. After a failure, the API answers 503 for 5 seconds before it tries again. | Check `AUTH_OIDC_<NAME>_ISSUER` and the network. |
| A user lands on `/login?error=rate_limited` | The callback rate limit applies. | Read [Sign-in security](auth-security.md). |
| Flows break after a restart | Each process made its own key. | Set `AUTH_OIDC_COOKIE_KEY`. |

{: .note }
Before you revert the OIDC migration, set a password for each account that has none. The reverse migration refuses to run while these accounts exist. It never deletes them.

## See also

- [Accounts and sign-in](../auth.md)
- [Sign-in security](auth-security.md)
- [Secrets](../deploy/secrets.md) to store the client secret
- [spinup new](../site/commands/new.md) for `--auth-oidc`
