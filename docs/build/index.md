---
title: Build your app
nav_order: 3
has_children: true
has_toc: false
---

# Build your app

These guides show how to add features to the app that `spinup new` made.
Each page starts with one example that you can copy.
The examples create a new app each time, so they are not steps in the tutorial app.
In your own app, run only the lines that start with `spinup g` or `spinup module add`.

For example, the first command adds a CRUD API, a database table, and four web pages. The second command adds one page:

```console
$ spinup new todo --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g resource task title:string done:bool --dry-run
...
update web/src/lib/nav.gen.ts (managed)
...
$ spinup g page about --dry-run
create web/src/routes/about/+page.svelte (scaffold)
```

To add a feature that is a module, such as mail, run `spinup module add mail`.

## Start here

| I want to ... | Read |
| --- | --- |
| Add a table with a CRUD API and pages | [generate resource](../commands/generate-resource.md) |
| Change the pages, the sidebar, or the theme | [The web app](../web.md) |
| Run the tests of the API, the web app, and the services | [Test your app](testing.md) |

## Users and access

Read these in order: accounts first, then roles, then the audit trail that records who did what.

| I want to ... | Read |
| --- | --- |
| Let users register and sign in | [Accounts and sign-in](../auth.md) |
| Limit routes to admins or other roles | [Roles](roles.md) |
| Change the rate limits and the proxy settings | [Sign-in security](auth-security.md) |
| Sign in with Google, Okta, or another provider | [Sign in with an identity provider](oidc.md) |
| Record who changed what | [Audit trail](../audit.md) |

## More features

| I want to ... | Read |
| --- | --- |
| Send email and password reset links | [Mail](../mail.md) |
| Turn a feature on or off without a deploy | [Feature flags](flags.md) |
| Let a program call the API with a key and a quota | [API keys and plans](api-keys.md) |
| Let AI clients call typed tools over MCP | [MCP server](mcp.md) |
| Sell subscriptions and meter API calls with Stripe | [Billing with Stripe](billing.md) |
| Run work after the response | [Background jobs](jobs.md) |
| Add a worker in Rust, Python, or Node | [Sidecar services](../services.md) |
| Make PDF files | [PDF reports](../pdf-reports.md) |
| Show the API to other developers | [API reference page](../api-docs.md) |
| Ship the web app inside the Go binary | [Embed mode](../embed.md) |
| Use bun or the heyapi client | [Package manager and API client](../web-variants.md) |
| Add commands or generators from outside | [Plugins](../plugins.md) and [Executable plugins](../plugins-exec.md) |

To deploy the app, read [Deploy](../deploy/index.md). To run it in production, read [Operate](../operate/index.md).
