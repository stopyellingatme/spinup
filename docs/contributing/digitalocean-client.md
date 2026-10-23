# DigitalOcean OAuth client

`spinup deploy digital-ocean login` uses an OAuth public client (ADR 0017 §2).
The maintainers register the client once. A normal user does nothing.

## Register the client

1. Run `scripts/do-register-client.sh`. The script needs `curl` and `jq`.
2. The script sends `POST /v1/oauth/register` with `token_endpoint_auth_method: none`.
3. The script prints the client ID on stdout.
4. Put the client ID in `digitalocean.DefaultClientID` in `internal/cloud/digitalocean/env.go`.
5. Run `make test` for the root module, and commit the change.

The client ID is not a secret.
`SPINUP_DO_CLIENT_ID` overrides the constant. `SPINUP_DO_OAUTH_BASE` overrides the endpoint for tests.

## Keep the registration access token safe

The script prints the registration access token on stderr only.
Store the token in a password manager.
Never commit the token. Never paste it in an issue, a log, or CI output.
The token lets you change or delete the client later.

## Never reuse another client

Never use the client ID of `doctl`.
That client belongs to DigitalOcean, and its consent screen names `doctl`.
spinup always uses its own client.

## Check the scopes

The scopes are in `digitalocean.Scopes`.
After a change of the scopes, run the live sign-in test on a real account.
The live test confirms the exact scope strings.
