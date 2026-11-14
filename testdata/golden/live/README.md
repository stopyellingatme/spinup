# todo

Made with spinup: a Go API, PostgreSQL, and (unless the app is API-only) a SvelteKit web app.

## Quick start

Start everything in development: PostgreSQL and Mailpit in Docker, the migrations, the API
with live reload, and the web dev server:

```sh
spinup dev
```

Open the app URL that `spinup dev` prints (http://localhost:5173 by default).
Press Ctrl-C to stop. The Docker services keep running; `spinup dev --down` stops them too.

## Everyday commands

```sh
spinup g resource post title:string body:text   # a CRUD API and pages
spinup db migrate up                             # apply migrations
spinup test                                      # run the API tests
spinup upgrade --plan                            # preview a template upgrade
```
