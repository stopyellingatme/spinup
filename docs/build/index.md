---
title: Build your app
nav_order: 3
has_children: true
has_toc: false
---

# Build your app

These guides show how to add features to the app that `spinup new` made.
Each page starts with one example that you can copy.
For example, this command adds a CRUD API, a database table, and four web pages:

```console
$ spinup new todo --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g resource todo title:string done:bool --dry-run
...
update web/src/lib/nav.gen.ts (managed)
...
```

| I want to ... | Read |
| --- | --- |
| Change the pages, the sidebar, or the theme | [The web app](../web.md) |
| Use bun or the heyapi client | [Package manager and API client](../web-variants.md) |
| Let users register and sign in | [Accounts and sign-in](../auth.md) |
| Limit routes to admins or other roles | [Roles](roles.md) |
| Change the rate limits and the proxy settings | [Sign-in security](auth-security.md) |
| Sign in with Google, Okta, or another provider | [Sign in with an identity provider](oidc.md) |
| Send email and password reset links | [Mail](../mail.md) |
| Run work after the response | [Background jobs](jobs.md) |
| Add a worker in Rust, Python, or Node | [Sidecar services](../services.md) |
| Make PDF files | [PDF reports](../pdf-reports.md) |
| Record who changed what | [Audit trail](../audit.md) |
| Show the API to other developers | [API reference page](../api-docs.md) |
| Run the tests of the API, the web app, and the services | [Test your app](testing.md) |
| Ship the web app inside the Go binary | [Embed mode](../embed.md) |
| Add commands or generators from outside | [Plugins](../plugins.md) and [Executable plugins](../plugins-exec.md) |

The guide for that command is [generate resource](../commands/generate-resource.md).
To deploy the app, read [Deploy](../deploy/index.md). To run it in production, read [Operate](../operate/index.md).
