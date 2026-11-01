---
title: DigitalOcean OAuth client
parent: Contributing
nav_order: 11
---

# DigitalOcean OAuth client

`spinup deploy digital-ocean login` signs in with an OAuth public client that the maintainers registered once.
A normal user does nothing.
The registered client ID is already in the code, so register a new client only if you lose control of the old one.
This page shows how to register the client and how the fake DigitalOcean API (`dofake`) stays faithful to the real API.

## Register the client

1. Run the script. It needs `curl` and `jq`.
2. The script sends `POST /v1/oauth/register` with `token_endpoint_auth_method: none`.
3. The script prints the client ID on stdout.
4. Put the client ID in `digitalocean.DefaultClientID` in `internal/cloud/digitalocean/env.go`.
5. Run `make test` for the root module, and commit the change.

<!-- doctest: skip -->
```console
$ scripts/do-register-client.sh
2dfe39adee98fc5e3eaf5266e1b554daf47e2ef1b96dc47983fafe04e7637ce1
registration_access_token: <token>
Keep the registration access token in a password manager. Never commit it.
```

The script prints the client ID on stdout. It prints the last two lines on stderr.
The client ID above is the one that spinup uses today.

The client ID is not a secret.
To try another client without a code change, set `SPINUP_DO_CLIENT_ID` for one command.
`SPINUP_DO_OAUTH_BASE` overrides the OAuth endpoint, and the tests use it to reach the fake.

```sh
SPINUP_DO_CLIENT_ID=<client-id> spinup deploy digital-ocean login
```

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

## Fake API fidelity

`dofake` is the fake DigitalOcean API.
It models each rule that can change the result of a spinup call.
It does not model a rule that spinup can never meet.
Each modeled rule has a test in `internal/cloud/digitalocean/fidelity_test.go` or in the `dofake` tests.

Source for the rules: the DigitalOcean OpenAPI specification, `https://github.com/digitalocean/openapi/tree/main/specification/resources`, and the API reference, `https://docs.digitalocean.com/reference/api/digitalocean/`.
The specification does not give some rules (error bodies, the TTL minimum).
A row says "not documented" for such a rule.
The [live test](do-live-test.md) is the final check.

| Endpoint | Rule | Modeled | Source |
|---|---|---|---|
| all `/v2/` | Error body has `id`, `message`, and `request_id`. The client reads the request ID from the body when the header is missing. | yes | `shared/responses/unexpected_error.yml` |
| all `/v2/` | Answers carry `ratelimit-limit`, `ratelimit-remaining`, and `ratelimit-reset` (epoch seconds). | yes | API reference, "Rate Limit" |
| all `/v2/` | A 429 answer carries `Retry-After`. The client waits and retries, except for POST. | yes | API reference, "Rate Limit" |
| all `/v2/` | A missing or bad token answers 401. | yes | `shared/responses/unauthorized.yml` |
| list endpoints | `per_page` has a maximum of 200. The answer has `links.pages.next` and `meta.total`. | yes | API reference, "Links and Pagination" |
| `GET /v2/account` | Returns the team and the droplet limit. | yes | `account/account_get.yml` |
| `POST /v2/account/keys` | 201 on create. A key that exists answers 422. A bad key answers 422. | yes | `ssh_keys/sshKeys_create.yml` |
| `POST /v2/account/keys` | A duplicate key name. | no, not documented. A test asserts that the client keeps the 422 as an error. | `ssh_keys/models/ssh_key.yml` |
| `GET /v2/account/keys/{fingerprint}` | 404 for an unknown key. | yes | `ssh_keys/sshKeys_get.yml` |
| `POST /v2/droplets` | 202 on create. The droplet starts as `new`, then becomes `active` with an IPv4 address. | yes | `droplets/droplets_create.yml` |
| `POST /v2/droplets` | The name matches `^[a-zA-Z0-9]?[a-z0-9A-Z.\-]*[a-z0-9A-Z]$`. A name that ends with a dash gets 422. | yes. This showed a bug: `--env prod-` made a bad droplet name. The environment pattern now refuses a trailing dash. | `droplets/models/droplet_single_create.yml` |
| `POST /v2/droplets` | Tags must match the tag rules. The create makes missing tags. | yes | `tags/models/tags.yml` |
| `POST /v2/droplets` | Region, size, and SSH key must exist. The droplet limit gives 422. | yes | `droplets/models/droplet_create.yml` |
| `POST /v2/droplets` | `names` (many droplets), `vpc_uuid`, `volumes`, `backups`. | no, spinup creates one droplet and never sets these fields. | `droplets/models/droplet_multi_create.yml` |
| `GET /v2/droplets?tag_name=` | Filter by tag. | yes | `droplets/droplets_list.yml` |
| `DELETE /v2/droplets/{id}` | 204. A droplet that does not exist gets 404. | yes | `droplets/droplets_destroy.yml` |
| `/v2/droplets/{id}/actions` | Droplet actions. | no, spinup never calls them. | `droplet_actions/` |
| `POST /v2/tags` | 201 on create. The name matches `^[a-zA-Z0-9_\-:]+$` and has at most 255 characters. | yes | `tags/models/tags.yml` |
| `POST /v2/tags` | A tag that exists answers 422. | yes. The client treats the answer as success after it looks the tag up. Not documented in the specification. | `tags/tags_create.yml` |
| `POST /v2/tags` | Tag names are case stable (the first capitalization is canonical). | no, spinup tags are lower case and one name is always written the same way. | `tags/models/tags.yml` |
| `DELETE /v2/tags/{name}` | 204. An unknown tag gets 404. | yes | `tags/tags_delete.yml` |
| `POST /v2/firewalls` | A tag in the body must exist (422). | yes (found in the first live test). | `firewalls/models/firewall.yml` |
| `POST /v2/firewalls` | The name matches `^[a-zA-Z0-9][a-zA-Z0-9\.-]+$`. | yes | `firewalls/models/firewall.yml` |
| `POST`, `PUT /v2/firewalls` | The protocol is `tcp`, `udp`, or `icmp`. Ports are a port, a range, or `0` for all ports. | yes | `firewalls/models/firewall_rule.yml` |
| `POST`, `PUT /v2/firewalls` | The answer shows `0` for all ports and for ICMP. spinup sends `all` or no ports. The client compares both forms as equal. | yes | `firewalls/models/firewall_rule.yml` |
| `POST`, `PUT /v2/firewalls` | 202 on create, and the status is `waiting` until the change applies, then `succeeded`. spinup never reads the status. | yes. The next read shows `succeeded`. | `firewalls/models/firewall.yml` |
| `POST`, `PUT /v2/firewalls` | A rule source can be a droplet ID, a load balancer, or a tag. | no, spinup sends addresses only. | `firewalls/models/firewall_rule.yml` |
| `DELETE /v2/firewalls/{id}` | 204. An unknown firewall gets 404. | yes | `firewalls/firewalls_delete.yml` |
| `GET /v2/domains` | Lists the zones. | yes | `domains/domains_list.yml` |
| `POST /v2/domains/{zone}/records` | 201 on create. The apex name is `@`. The `name` filter on list takes the full name. | yes | `domains/domains_create_record.yml` |
| `POST /v2/domains/{zone}/records` | A minimum for `ttl`. | no, not documented. spinup always sends 300. | `domains/models/domain_record.yml` |
| `POST /v2/domains/{zone}/records` | The data of an A record is an IPv4 address. | no, spinup sends the public IPv4 address of the droplet. | `domains/models/domain_record.yml` |
| `PUT`, `DELETE .../records/{id}` | 200 on update, 204 on delete, 404 for an unknown record. | yes | `domains/domains_update_record.yml` |
| `GET /v2/sizes`, `GET /v2/regions` | Paginated lists. | yes | `sizes/sizes_list.yml`, `regions/regions_list.yml` |
| `GET /v1/oauth/authorize` | PKCE with S256 is required. The redirect goes to a loopback address. `access_denied` and `invalid_request` come back in the redirect query. | yes | `docs.digitalocean.com/reference/api/oauth/` |
| `POST /v1/oauth/token` | The answer has `access_token`, `token_type` (`bearer`), `expires_in`, `refresh_token`, and `scope`. A code works once. | yes | OAuth reference |
| `POST /v1/oauth/token` | A refresh gives a new refresh token, and the old one stops working (`invalid_grant`). A bad request gives `invalid_request`. | yes | OAuth reference |
| `POST /v1/oauth/revoke` | Revoke drops the access and the refresh token. An unknown token still answers 200. | yes | RFC 7009 |

The OAuth rows rest on the first live test and on the RFCs.

## See also

- [Run the DigitalOcean live test](do-live-test.md)
- [Docker-in-Docker deploy suite](dind.md), which runs the deploy flow against `dofake`
- [Deploy to DigitalOcean](../deploy-digitalocean.md)
